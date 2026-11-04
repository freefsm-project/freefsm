package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestLeaseAdjustmentSettledReductionAndBoundariesHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var paid, asset int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&paid))
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
	path := fmt.Sprintf("/leases/%d/billing/adjustments", lease)
	proposal := func(invoice int64) url.Values {
		t.Helper()
		body := requestBody(t, web, cookie, "GET", fmt.Sprintf("%s?invoice_id=%d", path, invoice), 200)
		return url.Values{"invoice_id": {fmt.Sprint(invoice)}, "asset_id": {fmt.Sprint(asset)}, "kind": {"abatement"}, "amount_cents": {"100"}, "starts_on": {"2024-01-31"}, "ends_on": {"2024-01-31"}, "reason": {"Paid outage reduction against future Invoice"}, "operation_key": {jobExecutionHidden(t, body, "operation_key")}, "invoice_revision": {jobExecutionHidden(t, body, "invoice_revision")}}
	}
	stale := proposal(paid)
	historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}, paid))
	_, err = settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "adjustment-valid-receipt-0001"}, InvoiceID: paid, AmountCents: 106, Method: settlement.Cash, ReceivedDate: "2024-01-31"})
	historyCheck(t, err)
	original := historyDurableRows(t, pool, []string{"invoices", "invoice_payments", "payment_invoice_allocations"})
	fieldPost(t, web, cookie, path, stale, 409)
	historyEqual(t, "settled facts never reduced", historyDurableRows(t, pool, []string{"invoices", "invoice_payments", "payment_invoice_allocations"}), original)
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-02'::date $$`)
	historyCheck(t, err)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var future int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT max(id) FROM invoices WHERE lease_id=$1 AND id<>$2`, lease, paid).Scan(&future))
	cash := historyDurableRows(t, pool, []string{"invoice_payments", "payment_invoice_allocations"})
	v := proposal(future)
	fieldPost(t, web, cookie, path, v, 303)
	fieldPost(t, web, cookie, path, v, 303)
	var total string
	historyCheck(t, pool.QueryRow(ctx, `SELECT financial::jsonb->'result'->>'total_cents' FROM invoices WHERE id=$1`, future).Scan(&total))
	historyEqual(t, "future Invoice reduced by explicit 100 cents", total, "5150")
	historyEqual(t, "valid receipt and allocation retained", historyDurableRows(t, pool, []string{"invoice_payments", "payment_invoice_allocations"}), cash)
	var n int
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM customer_credits)+(SELECT count(*) FROM settlement_reversals)`).Scan(&n))
	historyEqual(t, "no noncash Credit or reversal", n, 0)
	t.Run("concurrent reviewed proposals one effect", func(t *testing.T) {
		review, e := svc.ReviewLeaseAdjustments(ctx, actor, lease, future)
		historyCheck(t, e)
		results := make(chan error, 2)
		for i := range 2 {
			go func(i int) {
				results <- svc.ApplyLeaseAdjustment(ctx, actor, lease, authorization.LeaseAdjustmentInput{InvoiceID: future, AssetID: asset, InvoiceRevision: review.Selected.Revision, Kind: "damages", AmountCents: 25, StartsOn: "2024-02-01", EndsOn: "2024-02-01", Reason: "Explicit damages", OperationKey: fmt.Sprintf("adjustment-concurrent-%04d", i)})
			}(i)
		}
		accepted := 0
		for range 2 {
			if <-results == nil {
				accepted++
			}
		}
		historyEqual(t, "one stale competitor rejected", accepted, 1)
	})
	t.Run("queued Invoice archive", func(t *testing.T) {
		values := proposal(future)
		flow := statusflow.New(pool)
		fa := statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}
		historyCheck(t, flow.FinalizeInvoice(ctx, fa, future))
		var void int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='invoice:void' AND company_id=$1 AND is_category_default`, company).Scan(&void))
		historyCheck(t, flow.TransitionInvoice(ctx, fa, future, void))
		queuedChildPost(t, pool, web, cookie, path, `UPDATE invoices SET deleted_at=clock_timestamp() WHERE id=$1`, []any{future}, 403, values)
		historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_manual_adjustments WHERE lease_id=$1`, lease).Scan(&n))
		historyEqual(t, "archive rejected complete application", n, 2)
	})
	t.Run("authenticated field API cannot expose or mutate", func(t *testing.T) {
		token, e := sessions.CreateMobile(ctx, admin, "Adjustment denial device")
		historyCheck(t, e)
		api := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewLeaseDirectory(objectref.NewEntDirectory(client), pool), t.TempDir(), 0))
		for _, method := range []string{"GET", "POST"} {
			r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != 404 && w.Code != 405 {
				t.Fatalf("field API allowed adjustment: %d", w.Code)
			}
			assertNotContains(t, w.Body.String(), "Paid outage reduction")
		}
	})
}

func TestLeaseAdjustmentPopulatedUpgrade(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	tables := []string{"leases", "lease_finalizations", "lease_asset_dependencies", "lease_billing_obligations", "lease_invoice_generations", "lease_billing_revisions", "invoices", "assets", "customers", "authorization_roles", "authorization_grants", "authorization_audit", "issued_document_numbers"}
	before := leasePreservationRows(t, pool, tables)
	_, err := pool.Exec(ctx, `DROP TRIGGER lease_adjustment_customer_guard ON invoices; DROP FUNCTION lease_adjustment_customer_guard(); DROP TABLE lease_manual_adjustments; DELETE FROM schema_migrations WHERE name='124_lease_manual_adjustments'`)
	historyCheck(t, err)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "installed identities facts and grants preserved", leasePreservationRows(t, pool, tables), before)
	var n int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_manual_adjustments`).Scan(&n))
	historyEqual(t, "no invented adjustment history", n, 0)
}

func TestLeaseAdjustmentOrdinaryInvoiceCustomerHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var customer, asset, status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id FROM leases WHERE id=$1`, lease).Scan(&customer))
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:draft' AND is_category_default`, company).Scan(&status))
	ordinary := client.Invoice.Create().SetCompanyID(company).SetCustomerID(customer).SetStatusID(status).SetTitle("Explicit future Invoice").SetInvoiceNumber(7201).SetInvoiceDate(time.Now()).SetDueDate(time.Now().AddDate(0, 1, 0)).SetSettlementState("paid").SaveX(ctx)
	path := fmt.Sprintf("/leases/%d/billing/adjustments?invoice_id=%d", lease, ordinary.ID)
	body := requestBody(t, web, cookie, "GET", path, 200)
	values := url.Values{"invoice_id": {fmt.Sprint(ordinary.ID)}, "asset_id": {fmt.Sprint(asset)}, "kind": {"fee"}, "amount_cents": {"10001"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-01-01"}, "reason": {"Explicit ordinary future Invoice fee"}, "operation_key": {jobExecutionHidden(t, body, "operation_key")}, "invoice_revision": {jobExecutionHidden(t, body, "invoice_revision")}}
	fieldPost(t, web, cookie, path, values, 303)
	var total string
	historyCheck(t, pool.QueryRow(ctx, `SELECT financial::jsonb->'result'->>'total_cents' FROM invoices WHERE id=$1`, ordinary.ID).Scan(&total))
	historyEqual(t, "exact cents accepted through ordinary calculator", total, "10001")
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Wrong Customer").SaveX(ctx)
	if _, err := pool.Exec(ctx, `UPDATE invoices SET customer_id=$2 WHERE id=$1`, ordinary.ID, other.ID); err == nil {
		t.Fatal("manual provenance Customer changed")
	}
	foreign := client.Invoice.Create().SetCompanyID(company).SetCustomerID(other.ID).SetStatusID(status).SetTitle("Other Customer Invoice").SetInvoiceNumber(7202).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetSettlementState("paid").SaveX(ctx)
	foreignPath := fmt.Sprintf("/leases/%d/billing/adjustments?invoice_id=%d", lease, foreign.ID)
	requestBody(t, web, cookie, "GET", foreignPath, 404)
	values.Set("invoice_id", fmt.Sprint(foreign.ID))
	values.Set("operation_key", "wrong-customer-adjustment-0001")
	fieldPost(t, web, cookie, foreignPath, values, 404)
	historyCheck(t, authorization.New(pool).GenerateLeaseDrafts(ctx))
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations WHERE invoice_id=$1`, ordinary.ID).Scan(&count))
	historyEqual(t, "manual application never becomes monthly generation", count, 0)
}
