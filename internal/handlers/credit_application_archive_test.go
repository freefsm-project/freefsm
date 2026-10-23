package handlers

import (
	"context"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCreditApplicationArchivedCustomerLegacyWriters(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archived settlement history").SaveX(ctx)
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Archived settlement history"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice int64
	var credit, payment uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id,source_payment_id FROM customer_credits`).Scan(&credit, &payment))
	historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice))
	svc := settlement.New(pool)
	actor := settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
	date := settlement.Date("2026-01-02")
	refund, err := svc.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, CustomerID: c.ID, AmountCents: 1000, Method: settlement.Cash, EffectiveDate: date, Reason: "Historical refund"})
	historyCheck(t, err)
	_, err = svc.ReverseRefund(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: refund.ID, CustomerID: c.ID, EffectiveDate: date, Reason: "Historical correction"})
	historyCheck(t, err)
	app, err := svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, CreditID: credit, RequestedCents: 10000, EffectiveDate: date})
	historyCheck(t, err)
	// No outstanding due or available Credit remains, so the Customer can be
	// archived while preserving paid history. Reversals must not recreate either.
	_, err = client.Customer.UpdateOneID(c.ID).SetDeletedAt(time.Now()).Save(ctx)
	historyCheck(t, err)
	for name, write := range map[string]func() (settlement.Result, error){
		"payment": func() (settlement.Result, error) {
			return svc.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, AmountCents: 1, Method: settlement.Cash, ReceivedDate: date})
		},
		"apply": func() (settlement.Result, error) {
			return svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, CreditID: credit, RequestedCents: 1, EffectiveDate: date})
		},
		"refund": func() (settlement.Result, error) {
			return svc.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, CustomerID: c.ID, AmountCents: 1, Method: settlement.Cash, EffectiveDate: date, Reason: "refund"})
		},
		"reverse-payment": func() (settlement.Result, error) {
			return svc.ReversePayment(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: payment, CustomerID: c.ID, EffectiveDate: date, Reason: "correction"})
		},
		"reverse-application": func() (settlement.Result, error) {
			return svc.ReverseCreditApplication(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: app.ID, InvoiceID: invoice, EffectiveDate: date, Reason: "correction"})
		},
		"reverse-refund": func() (settlement.Result, error) {
			return svc.ReverseRefund(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: refund.ID, CustomerID: c.ID, EffectiveDate: date, Reason: "correction"})
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := write(); !errors.Is(err, settlement.ErrCustomerArchived) {
				t.Fatalf("archived writer: %v", err)
			}
		})
	}
	page := documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d/credit", invoice), nil, 200)
	assertContains(t, page.Body.String(), app.ID.String())
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/invoices/%d/credit-applications/%s/reverse", invoice, app.ID), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"correction"}}, 409)
	var applications, reversals, payments int
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM credit_applications),(SELECT count(*) FROM settlement_reversals),(SELECT count(*) FROM invoice_payments)`).Scan(&applications, &reversals, &payments))
	if applications != 1 || reversals != 1 || payments != 1 {
		t.Fatalf("archived writes changed history: %d %d %d", applications, reversals, payments)
	}
}

func TestCreditApplicationArchivedCustomerHTTPWriters(t *testing.T) {
	f := newCreditMatrix(t)
	f.post(t, f.apply(f.invoice, "50.00"), 303)
	f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/payments-with-credit", f.invoice), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"50.00"}, "method": {"cash"}, "date": {"2026-01-02"}}}, 303)
	f.post(t, creditCommand{fmt.Sprintf("/customers/%d/credit/refunds", f.customer), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"50.00"}, "method": {"cash"}, "effective_date": {"2026-01-02"}, "reason": {"external refund"}}}, 303)
	var application, refund, payment string
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_applications`).Scan(&application))
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_refunds`).Scan(&refund))
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM invoice_payments WHERE invoice_id=$1`, f.invoice).Scan(&payment))
	base := fmt.Sprintf("/customers/%d", f.customer)
	invoice := fmt.Sprintf("/invoices/%d", f.invoice)
	var preview struct{ Snapshot string }
	masterJSON(t, f.web, f.cookie, base+"/archive", &preview)
	f.post(t, creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}, 303)
	tables := []string{"invoice_payments", "payment_invoice_allocations", "customer_credits", "credit_applications", "credit_refunds", "credit_refund_allocations", "invoices"}
	before := historyDurableRows(t, f.pool, tables)
	for _, path := range []string{invoice + "/payments", invoice + "/payments-with-credit", base + "/payments", invoice + "/credit-applications", invoice + "/credit-applications/" + application + "/reverse", invoice + "/payments/" + payment + "/reverse", base + "/payments/" + f.payment.String() + "/reverse", base + "/credit/refunds", base + "/credit/refunds/" + refund + "/reverse"} {
		t.Run(path, func(t *testing.T) {
			cmd := creditCommand{path, url.Values{"idempotency_key": {newOperationKey()}, "credit_id": {f.source.String()}, "amount": {"1.00"}, "method": {"cash"}, "date": {"2026-01-02"}, "effective_date": {"2026-01-02"}, "reason": {"new operation after archive"}}}
			if strings.HasSuffix(path, "/payments") || strings.HasSuffix(path, "/payments-with-credit") {
				cmd.values.Del("credit_id")
				cmd.values.Del("effective_date")
				cmd.values.Del("reason")
			}
			w := f.call(cmd)
			if w.Code != 403 && w.Code != 409 && w.Code != 422 {
				t.Fatalf("archived writer returned%d: %s", w.Code, w.Body.String())
			}
			historyEqual(t, "archived writer preserves money and Invoice facts", historyDurableRows(t, f.pool, tables), before)
		})
	}
	f.balances(t, 0, 0, 15000)
	documentTransactionRequest(t, f.web, f.cookie, "GET", invoice+"/credit", nil, 200)
	documentTransactionRequest(t, f.web, f.cookie, "GET", base+"/payments", nil, 200)
	f.post(t, creditCommand{base + "/restore", nil}, 303)
	f.balances(t, 0, 0, 15000)
	f.post(t, creditCommand{base + "/credit/refunds/" + refund + "/reverse", url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"explicit correction after restore"}}}, 303)
	f.balances(t, 0, 5000, 15000)
	f.post(t, creditCommand{invoice + "/credit-applications/" + application + "/reverse", url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"explicit correction after restore"}}}, 303)
	f.balances(t, 5000, 10000, 15000)
}
