package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
)

// This seam starts with real office HTTP finalization and observes ordinary
// Invoices plus the retained pending identity after a simulated outage.
func TestLeaseBillingWorkerCatchupHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	clock := func(date string) {
		t.Helper()
		_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+date+`'::date $$`)
		historyCheck(t, err)
	}
	clock("2024-01-01")
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Worker Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Covered").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-12-31"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	amount, rent := int64(100), int64(5000)
	svc := authorization.New(pool)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id, 0, "2024-01-01", authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &amount, RentCents: &rent, TaxRate: "5"}, 0))
	grants := []authorization.Grant{}
	for _, key := range []string{"lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage"} {
		grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Billing approver", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Approver").SetEmail("worker67@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie = sessionCookie(t, ctx, sessions, u.ID)
	fieldPost(t, web, cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {"2024-01-01"}}, 303)
	var pending int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE kind='monthly'`).Scan(&pending))
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var early int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&early))
	historyEqual(t, "31 days before due remains ungenerated", early, 1)
	clock("2024-04-01")
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_worker_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.invoice.generate' THEN RAISE EXCEPTION 'injected worker audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_worker_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_worker_audit()`)
	historyCheck(t, err)
	if err := svc.GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("worker hid failed generation")
	}
	var failedCount int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&failedCount))
	historyEqual(t, "failed worker leaves only initial Invoice", failedCount, 1)
	var identities []int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY due_on,kind) FROM lease_billing_obligations`).Scan(&identities))
	historyEqual(t, "failed periods retain approved obligations", len(identities), 5)
	if err := authorization.New(pool).GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("restart hid repeated fault")
	}
	var replayIDs []int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY due_on,kind) FROM lease_billing_obligations`).Scan(&replayIDs))
	historyEqual(t, "failed retry identity", replayIDs, identities)
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/finalize", 200), "Draft generation failed")
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/billing", 200), "Draft generation failed")
	retryPath := fmt.Sprintf("%s/billing/periods/%d", path, pending)
	retryBody := requestBody(t, web, cookie, "GET", retryPath, 200)
	retryValues := url.Values{"review_hash": {jobExecutionHidden(t, retryBody, "review_hash")}, "operation_key": {jobExecutionHidden(t, retryBody, "operation_key")}}
	fieldPost(t, web, cookie, retryPath+"/retry", retryValues, 409)
	_, err = pool.Exec(ctx, `DROP TRIGGER reject_worker_audit ON authorization_audit`)
	historyCheck(t, err)
	fieldPost(t, web, cookie, retryPath+"/retry", retryValues, 303)
	fieldPost(t, web, cookie, retryPath+"/retry", retryValues, 303)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	// February, March, April and May (exactly 30 days away), plus initial.
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations`).Scan(&count))
	historyEqual(t, "all missed periods", count, 5)
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY due_on,kind) FROM lease_billing_obligations`).Scan(&replayIDs))
	historyEqual(t, "successful retry identity", replayIDs, identities)
	var retained bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_invoice_generations WHERE obligation_id=$1)`, pending).Scan(&retained))
	historyEqual(t, "pending identity reused", retained, true)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations`).Scan(&count))
	historyEqual(t, "lost response replay", count, 5)
	var invoice int64
	var correct bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT i.id,s.category_key='invoice:draft' AND i.lease_id=$1 AND i.line_items->0->>'asset_id'=$2 AND i.line_items->0->>'period'='2024-02-01' AND i.line_items->0->>'charge_kind'='monthly' AND i.financial::jsonb->'result'->>'total_cents'='5250' FROM invoices i JOIN statuses s ON s.id=i.status_id JOIN lease_invoice_generations g ON g.invoice_id=i.id WHERE g.obligation_id=$3`, id, fmt.Sprint(a.ID), pending).Scan(&invoice, &correct))
	historyEqual(t, "ordinary Draft totals and provenance", correct, true)
	requestBody(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d", invoice), 403)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE actor_kind='lease-billing-worker' AND actor_id IS NULL AND role_id IS NULL AND action='lease.invoice.generate'`).Scan(&count))
	historyEqual(t, "worker attribution without impersonation", count, 3)
	var void int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' AND is_category_default`, company).Scan(&void))
	flow := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}
	historyCheck(t, flow.FinalizeInvoice(ctx, actor, invoice))
	historyCheck(t, flow.TransitionInvoice(ctx, actor, invoice, void))
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations`).Scan(&count))
	historyEqual(t, "Void never automatically replaces", count, 5)
	reviewPath := fmt.Sprintf("%s/billing/periods/%d", path, pending)
	body := requestBody(t, web, cookie, "GET", reviewPath, 200)
	assertContains(t, body, "Review replacement")
	values := url.Values{}
	for _, key := range []string{"invoice_id", "review_hash", "operation_key"} {
		values.Set(key, jobExecutionHidden(t, body, key))
	}
	for _, key := range []string{"lease.view", "lease.manage", "lease.billing.manage", "lease.billing.view"} {
		t.Run("replacement queued revocation "+key, func(t *testing.T) {
			queuedChildPost(t, pool, web, cookie, reviewPath+"/replace", `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, []any{role, key}, 403, values)
			_, err := pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,'A')`, role, key)
			historyCheck(t, err)
		})
	}
	var oldBytes string
	historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1`, invoice).Scan(&oldBytes))
	fieldPost(t, web, cookie, reviewPath+"/replace", values, 303)
	fieldPost(t, web, cookie, reviewPath+"/replace", values, 303)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var replacement int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM lease_invoice_generations WHERE predecessor_invoice_id=$1`, invoice).Scan(&replacement))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations`).Scan(&count))
	historyEqual(t, "replacement replay and worker keep one successor", count, 6)
	var retainedBytes string
	historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1`, invoice).Scan(&retainedBytes))
	historyEqual(t, "replacement preserves original Invoice bytes", retainedBytes, oldBytes)
	assertContains(t, requestBody(t, web, cookie, "GET", reviewPath, 200), fmt.Sprintf("Invoice %d", replacement))
	var activeStatus int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced' AND is_category_default`, company).Scan(&activeStatus))
	if err := flow.TransitionInvoice(ctx, actor, invoice, activeStatus); err == nil {
		t.Fatal("superseded Void Invoice reactivated")
	}
	beforeChanges := historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations"})
	end := "2024-03-31"
	historyCheck(t, svc.WithinActor(ctx, authorization.Actor{UserID: u.ID}, func(tx pgx.Tx, _ authorization.Principal) error {
		if err := svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: u.ID}, authorization.LeaseDependencies{LeaseID: id, CustomerID: c.ID, PremisesID: &l.ID, Lifecycle: "active", Coverage: []authorization.LeaseAssetDependency{{AssetID: a.ID, StartsOn: "2024-01-01", EndsOn: &end}}}); err != nil {
			return err
		}
		return svc.RecordLeaseBillingSourceChange(ctx, tx, authorization.Actor{UserID: u.ID}, id, "2024-06-01", nil, "Coverage ends on March 31; June onward has no covered Assets")
	}))
	historyEqual(t, "coverage change preserves generated bytes", historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations"}), beforeChanges)
	var flagged []string
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(o.due_on::text ORDER BY o.due_on) FROM lease_invoice_review_flags r JOIN lease_invoice_generations g ON g.invoice_id=r.invoice_id JOIN lease_billing_obligations o ON o.id=g.obligation_id`).Scan(&flagged))
	historyEqual(t, "only changed inclusive coverage periods flagged", flagged, []string{"2024-04-01", "2024-05-01"})
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/billing", 200), "Manual review required")
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/billing/history", 200), "June onward has no covered Assets")
	if strings.Contains(requestBody(t, web, cookie, "GET", path+"/history", 200), "June onward has no covered Assets") {
		t.Fatal("billing change reason leaked into base history")
	}
	clock("2024-06-01")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyEqual(t, "no coverage does not generate more Invoices", historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations"}), beforeChanges)
	var omitted int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_period_dispositions`).Scan(&omitted))
	historyEqual(t, "June and July are explicit non-owed periods", omitted, 2)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
}
