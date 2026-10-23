package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestRefundIndependentAuthorityAndAuditHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "credit.view", Scope: authorization.All}, {Permission: "settlement.view", Scope: authorization.All}, {Permission: "credit.refund", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Refund clerk", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("refund@test").SetName("Refund clerk").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Refund owner").SaveX(ctx)
	path := fmt.Sprintf("/customers/%d/credit/refunds", c.ID)
	documentTransactionRequest(t, web, adminCookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	v := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"60.00"}, "method": {"transfer"}, "effective_date": {"2026-01-03"}, "reason": {"Returned deposit"}, "reference": {"bank-refund"}, "notes": {"Already completed externally"}}
	_, err = pool.Exec(ctx, `CREATE FUNCTION refund_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='credit.refund' THEN RAISE EXCEPTION 'injected refund audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER refund_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION refund_audit_failure()`)
	historyCheck(t, err)
	documentTransactionRequest(t, web, cookie, "POST", path, v, 500)
	var effects int
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM credit_refunds)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)`, v.Get("idempotency_key")).Scan(&effects))
	if effects != 0 {
		t.Fatalf("failed audit retained %d effects", effects)
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER refund_audit_failure ON authorization_audit; DROP FUNCTION refund_audit_failure()`)
	historyCheck(t, err)
	documentTransactionRequest(t, web, cookie, "POST", path, v, 303)
	documentTransactionRequest(t, web, cookie, "POST", path, v, 303)
	v.Set("amount", "61.00")
	documentTransactionRequest(t, web, cookie, "POST", path, v, 409)
	var refund string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM credit_refunds`).Scan(&refund))
	r := url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-03"}, "reason": {"Incorrect external record"}}
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+refund+"/reverse", r, 403)
	documentTransactionRequest(t, web, adminCookie, "POST", path+"/"+refund+"/reverse", r, 303)
	documentTransactionRequest(t, web, adminCookie, "POST", path+"/"+refund+"/reverse", r, 303)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action IN ('credit.refund','credit.refund.reverse')`).Scan(&effects))
	if effects != 2 {
		t.Fatalf("refund/reversal audits=%d want2", effects)
	}
}

func TestRefundFIFOAndPaymentDependencyHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("FIFO refund").SaveX(ctx)
	for _, source := range []struct{ amount, date string }{{"40.00", "2026-01-02"}, {"80.00", "2026-01-03"}} {
		documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {source.amount}, "method": {"cash"}, "date": {source.date}}, 303)
	}
	path := fmt.Sprintf("/customers/%d/credit/refunds", c.ID)
	v := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"70.00"}, "method": {"check"}, "effective_date": {"2026-01-04"}, "reason": {"External return"}}
	documentTransactionRequest(t, web, cookie, "POST", path, v, 303)
	var refund, payment string
	var first, second int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT f.id,(SELECT a.amount_cents FROM credit_refund_allocations a JOIN customer_credits c ON c.id=a.credit_id WHERE a.refund_id=f.id AND c.source_date='2026-01-02'),(SELECT a.amount_cents FROM credit_refund_allocations a JOIN customer_credits c ON c.id=a.credit_id WHERE a.refund_id=f.id AND c.source_date='2026-01-03') FROM credit_refunds f`).Scan(&refund, &first, &second))
	if first != 4000 || second != 3000 {
		t.Fatalf("FIFO portions=%d,%d", first, second)
	}
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoice_payments WHERE received_date='2026-01-02'`).Scan(&payment))
	reversePayment := fmt.Sprintf("/customers/%d/payments/%s/reverse", c.ID, payment)
	r := url.Values{"idempotency_key": {newOperationKey()}, "date": {"2026-01-04"}, "reason": {"error"}}
	documentTransactionRequest(t, web, cookie, "POST", reversePayment, r, 409)
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+refund+"/reverse", url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-04"}, "reason": {"Incorrect refund record"}}, 303)
	documentTransactionRequest(t, web, cookie, "POST", reversePayment, r, 303)
	documentTransactionRequest(t, web, cookie, "POST", reversePayment, r, 303)
	// The remaining exact source is restored, permitting its full original value.
	v.Set("idempotency_key", newOperationKey())
	v.Set("amount", "80.00")
	documentTransactionRequest(t, web, cookie, "POST", path, v, 303)
	var originals int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM credit_refund_allocations WHERE refund_id=$1`, refund).Scan(&originals))
	if originals != 2 {
		t.Fatalf("original source breakdown lost: %d", originals)
	}
}

func TestRefundInvoicePaymentIndependentReversalHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Payment correction").SaveX(ctx)
	documentTransactionRequest(t, web, adminCookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Original receipt target"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice))
	historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice))
	documentTransactionRequest(t, web, adminCookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "invoice_id": {fmt.Sprint(invoice)}, "amount": {"125.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	var payment string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoice_payments`).Scan(&payment))
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "settlement.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "payment.reverse", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Payment corrections", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("corrections@test").SetName("Corrections").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/invoices/%d/payments/%s/reverse", invoice, payment)
	r := url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-03"}, "reason": {"chargeback"}, "reference": {"bank-53"}, "notes": {"External bank notification"}}
	documentTransactionRequest(t, web, cookie, "POST", path, r, 303)
	documentTransactionRequest(t, web, cookie, "POST", path, r, 303)
	r.Set("notes", "Changed correction")
	documentTransactionRequest(t, web, cookie, "POST", path, r, 409)
	var amount, allocation, credit int64
	var state, reference, notes string
	historyCheck(t, pool.QueryRow(ctx, `SELECT p.amount_cents,a.amount_cents,c.original_amount_cents,i.settlement_state,r.reference,r.notes FROM invoice_payments p JOIN payment_invoice_allocations a ON a.payment_id=p.id JOIN customer_credits c ON c.source_payment_id=p.id JOIN invoices i ON i.id=p.invoice_id JOIN settlement_reversals r ON r.operation_id=p.id WHERE p.id=$1`, payment).Scan(&amount, &allocation, &credit, &state, &reference, &notes))
	if amount != 12500 || allocation != 10000 || credit != 2500 || state != "unpaid" || reference != "bank-53" || notes != "External bank notification" {
		t.Fatalf("original/compensation: %d %d %d %s %s %s", amount, allocation, credit, state, reference, notes)
	}
}
