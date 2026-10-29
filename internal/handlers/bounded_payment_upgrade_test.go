package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/testutil"
	"github.com/google/uuid"
)

func TestBoundedPaymentUpgradeRetainsInstalledReceipts(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprintf("cross_family_collision_%v", collision), func(t *testing.T) { testInstalledPaymentUpgrade(t, collision) })
	}
}

func TestBoundedPaymentInstalledReceiptAmbiguityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Ambiguous installed operation").SaveX(ctx)
	var status int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced'`, company).Scan(&status); err != nil {
		t.Fatal(err)
	}
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Retained ambiguous Payments").SetStatusID(status).SetInvoiceDate(time.Now()).SetDueDate(time.Time{}).SetLineItems(`[{"title":"Work","quantity":1,"unit_price":100}]`).SaveX(ctx)
	op := "50000000-0000-4000-a000-000000000017"
	for _, key := range []string{op, strings.ToUpper(op)} {
		if _, err := settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: key}, InvoiceID: i.ID, AmountCents: 1000, Method: settlement.Cash, ReceivedDate: "2026-01-02"}); err != nil {
			t.Fatal(err)
		}
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	documentTransactionRequest(t, router, cookie, "GET", "/payment-operations/"+op, nil, 409)
	documentTransactionRequest(t, router, cookie, "POST", fmt.Sprintf("/invoices/%d/payments", i.ID), url.Values{"idempotency_key": {op}, "amount": {"10.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 409)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invoice_payments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatal("ambiguity created another financial effect")
	}
}

func testInstalledPaymentUpgrade(t *testing.T, collision bool) {
	source := database.MigrationFS()
	old := fstest.MapFS{}
	files, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name() >= "087_" {
			continue
		}
		data, err := fs.ReadFile(source, f.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[f.Name()] = &fstest.MapFile{Data: data}
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed settlement Customer").SaveX(ctx)
	var status int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced'`, company).Scan(&status); err != nil {
		t.Fatal(err)
	}
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Installed receipt").SetStatusID(status).SetInvoiceDate(time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)).SetDueDate(time.Time{}).SetLineItems(`[{"title":"Work","quantity":1,"unit_price":100}]`).SaveX(ctx)
	id := uuid.New()
	op := newOperationKey()
	result := settlement.Result{ID: id, AppliedCents: 2500, State: settlement.PartiallyPaid}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	// Exact recorded pre-change request shape, independent of the current struct.
	legacy := fmt.Sprintf(`{"AmountCents":2500,"InvoiceID":%d,"Method":"cash","Notes":"installed note","ReceivedDate":"2020-01-02","Reference":"installed reference"}`, i.ID)
	digest := sha256.Sum256([]byte(legacy))
	if _, err := pool.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,invoice_id,amount_cents,method,received_date,reference,notes,actor_id,created_at) VALUES($1,$2,$3,$4,2500,'cash','2020-01-02','installed reference','installed note',$5,'2020-01-03T12:00Z');`, id, company, c.ID, i.ID, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO payment_invoice_allocations VALUES($1,$2,2500)`, id, i.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `SELECT settlement_set_invoice_state($1,'partially_paid')`, i.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO settlement_idempotency(company_id,operation,idempotency_key,request_fingerprint,result_id,result_json) VALUES($1,'record_payment',$5,$2,$3,$4)`, company, hex.EncodeToString(digest[:]), id, raw, op); err != nil {
		t.Fatal(err)
	}
	var bindingBefore string
	if collision {
		job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Installed collision Job").SaveX(ctx)
		token, err := sessions.CreateMobile(ctx, admin, "Installed other-family client")
		if err != nil {
			t.Fatal(err)
		}
		lifecycleCall(t, apiv1.NewRouter(pool, client, sessions), token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Installed other-family effect","line_items":[]}`, op, newOperationKey(), job.ID), 200)
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM operation_admissions a WHERE operation_id=$1`, op).Scan(&bindingBefore); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func() string {
		// Preserve the assertion through every receipt replay as well as upgrade.
		// Before 110 the key is absent; afterward only an explicit NULL is allowed.
		var invented bool
		historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoices i WHERE to_jsonb(i)->'lease_id' IS NOT NULL AND to_jsonb(i)->'lease_id'<>'null'::jsonb)`).Scan(&invented))
		if invented {
			t.Fatal("installed Invoice acquired invented Lease provenance")
		}
		var v string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('payments',(SELECT jsonb_agg(to_jsonb(p)-'method_label'-'other_description'-'channel' ORDER BY id) FROM invoice_payments p),'allocations',(SELECT jsonb_agg(to_jsonb(a) ORDER BY payment_id) FROM payment_invoice_allocations a),'idempotency',(SELECT jsonb_agg(to_jsonb(k)) FROM settlement_idempotency k),'invoices',(SELECT jsonb_agg(to_jsonb(i)-'lease_id' ORDER BY id) FROM invoices i),'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g))::text`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := snapshot()
	sequences := testutil.Sequences(t, ctx, pool)
	for range 2 {
		if err := (&database.DB{Pool: pool}).Migrate(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
	testutil.AssertNoInvoiceLeaseBackfill(t, ctx, pool)
	testutil.AssertSequenceUpgrade(t, ctx, pool, sequences, "lease_billing_obligations_id_seq", "leases_id_seq", "lease_billing_source_changes_id_seq", "lease_billing_obligation_revisions_id_seq")
	if snapshot() != before {
		t.Fatal("upgrade changed installed history, allocation, identities, projection or grants")
	}
	var unknown bool
	if err := pool.QueryRow(ctx, `SELECT method_label IS NULL AND other_description IS NULL AND channel IS NULL FROM invoice_payments WHERE id=$1`, id).Scan(&unknown); err != nil {
		t.Fatal(err)
	}
	if !unknown {
		t.Fatal("upgrade invented receipt metadata")
	}
	replayed, err := settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: op}, InvoiceID: i.ID, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: "2020-01-02", Reference: "installed reference", Notes: "installed note"})
	if err != nil {
		t.Fatalf("installed intent cannot reconcile: %v", err)
	}
	if replayed != result || snapshot() != before {
		t.Fatal("installed replay changed financial history")
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	lookup := documentTransactionRequest(t, router, cookie, "GET", "/payment-operations/"+op, nil, 200).Body.String()
	assertContains(t, lookup, id.String())
	assertNotContains(t, lookup, "installed note")
	assertNotContains(t, lookup, "installed reference")
	assertNotContains(t, lookup, "customer_id")
	role, err := authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "No receipt ownership", nil)
	if err != nil {
		t.Fatal(err)
	}
	other := client.User.Create().SetCompanyID(company).SetEmail("other-receipt@test").SetName("Other user").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, other.ID, role); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, router, sessionCookie(t, ctx, sessions, other.ID), "GET", "/payment-operations/"+op, nil, 404)
	values := url.Values{"idempotency_key": {op}, "amount": {"25.00"}, "method": {"cash"}, "date": {"2020-01-02"}, "reference": {"installed reference"}, "notes": {"installed note"}}
	want := 303
	if collision {
		want = 409
	}
	documentTransactionRequest(t, router, cookie, "POST", fmt.Sprintf("/invoices/%d/payments", i.ID), values, want)
	if snapshot() != before {
		t.Fatal("existing HTTP route repeated an installed Payment")
	}
	values.Set("amount", "25.01")
	documentTransactionRequest(t, router, cookie, "POST", fmt.Sprintf("/invoices/%d/payments", i.ID), values, 409)
	token, err := sessions.CreateMobile(ctx, admin, "Installed UUID collision")
	if err != nil {
		t.Fatal(err)
	}
	w := boundedAPICall(apiv1.NewRouter(pool, client, sessions), token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":1,"title":"Collision","line_items":[]}`, op, newOperationKey()))
	if w.Code != 409 {
		t.Fatalf("legacy UUID cross-family reuse=%d: %s", w.Code, w.Body.String())
	}
	assertContains(t, w.Body.String(), "operation_intent_changed")
	if snapshot() != before {
		t.Fatal("legacy identity collision changed installed facts")
	}
	api := apiv1.NewRouter(pool, client, sessions)
	if receipt := boundedAPICall(api, token, "GET", "/payment-operations/"+op, ""); receipt.Code != 200 {
		t.Fatalf("API original receipt=%d: %s", receipt.Code, receipt.Body.String())
	} else {
		assertContains(t, receipt.Body.String(), id.String())
		assertNotContains(t, receipt.Body.String(), "installed note")
	}
	otherToken, err := sessions.CreateMobile(ctx, other.ID, "Other receipt user")
	if err != nil {
		t.Fatal(err)
	}
	if denied := boundedAPICall(api, otherToken, "GET", "/payment-operations/"+op, ""); denied.Code != 404 {
		t.Fatalf("foreign receipt=%d: %s", denied.Code, denied.Body.String())
	}
	if collision {
		var bindingAfter string
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(a)::text FROM operation_admissions a WHERE operation_id=$1`, op).Scan(&bindingAfter); err != nil {
			t.Fatal(err)
		}
		if bindingAfter != bindingBefore {
			t.Fatal("historical common registry binding was rewritten")
		}
	}
}
