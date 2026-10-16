package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
)

func TestBoundedPaymentUpgradeRetainsInstalledReceipts(t *testing.T) {
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
	snapshot := func() string {
		var v string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('payments',(SELECT jsonb_agg(to_jsonb(p)-'method_label'-'other_description'-'channel' ORDER BY id) FROM invoice_payments p),'allocations',(SELECT jsonb_agg(to_jsonb(a) ORDER BY payment_id) FROM payment_invoice_allocations a),'idempotency',(SELECT jsonb_agg(to_jsonb(k)) FROM settlement_idempotency k),'invoices',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i),'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g))::text`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := snapshot()
	for range 2 {
		if err := (&database.DB{Pool: pool}).Migrate(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
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
	values := url.Values{"idempotency_key": {op}, "amount": {"25.00"}, "method": {"cash"}, "date": {"2020-01-02"}, "reference": {"installed reference"}, "notes": {"installed note"}}
	documentTransactionRequest(t, router, cookie, "POST", fmt.Sprintf("/invoices/%d/payments", i.ID), values, 303)
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
}
