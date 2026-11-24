package handlers

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
)

func TestFinancialAcceptanceSnapshotOverlappingRealRefund(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	base, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ctx := base
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Concurrent cash source").SaveX(ctx)
	ctx = authorization.WithActor(ctx, authorization.New(pool), authorization.Actor{UserID: admin, Channel: "web"})
	svc := settlement.New(pool)
	_, err := svc.RecordCustomerPayment(ctx, authorization.Actor{UserID: admin, Channel: "web"}, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 10000, Method: settlement.Cash, ReceivedDate: "2020-01-01"})
	historyCheck(t, err)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := financialAcceptancePost(t, web, cookie, "/reports/payments-credits/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	holder, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer holder.Rollback(context.Background())
	var blocker int32
	historyCheck(t, holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(100299)`).Scan(&blocker))
	_, err = pool.Exec(ctx, `CREATE FUNCTION hold_financial_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100299); RETURN NEW; END $$; CREATE TRIGGER hold_financial_snapshot BEFORE INSERT ON export_records FOR EACH ROW WHEN(NEW.ordinal=0) EXECUTE FUNCTION hold_financial_snapshot()`)
	historyCheck(t, err)
	generated := make(chan error, 1)
	go func() { _, err := exports.Product(pool).ProcessOne(ctx); generated <- err }()
	worker := timeExportDMLWaiter(t, ctx, pool, blocker, 100299, "INSERT INTO export_records")
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT (now() AT TIME ZONE timezone)::date::text FROM company_settings WHERE company_id=$1`, company).Scan(&today))
	refunded := make(chan error, 1)
	go func() {
		_, err := svc.RefundCredit(ctx, settlement.Actor{ID: admin, CompanyID: company}, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, CustomerID: c.ID, AmountCents: 2000, Method: settlement.Cash, EffectiveDate: settlement.Date(today), Reason: "Concurrent refund"})
		refunded <- err
	}()
	// A real command must wait at the installed authority/commercial fence. The
	// server lock graph witnesses overlap; timing/sleeps are not the assertion.
	waitAuthorityRequest(t, ctx, pool, worker, authorityWriterStatement)
	historyCheck(t, holder.Commit(ctx))
	historyCheck(t, <-generated)
	historyCheck(t, <-refunded)
	check := func(path, cash, credit string) {
		t.Helper()
		rows := timeExportCSV(t, requestBody(t, web, cookie, "GET", path+"/download", 200))
		known, balance := false, false
		for _, r := range rows {
			if r["as_of"] != rows[0]["as_of"] {
				t.Fatal("mixed snapshot stamps")
			}
			if r["row_type"] == "net_payments_total" && r["section"] == "dated_history" {
				known = true
				if r["cash_effect"] != cash {
					t.Fatalf("snapshot cash=%s want %s", r["cash_effect"], cash)
				}
			}
			if r["row_type"] == "current_credit_total" {
				balance = true
				if r["current_available_amount"] != credit {
					t.Fatalf("snapshot Credit=%s want %s", r["current_available_amount"], credit)
				}
			}
		}
		if !known || !balance {
			t.Fatal("snapshot missing independent totals")
		}
	}
	check(path, "100.00", "100.00")
	fresh := financialAcceptancePost(t, web, cookie, "/reports/payments-credits/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	_, err = exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	check(fresh, "80.00", "80.00")
	check(path, "100.00", "100.00") // data changes do not rewrite frozen bytes
}

func TestFinancialAcceptanceCompetingRefundAndApplicationConserveSource(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Contended source").SaveX(ctx)
	var issued int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='invoice:invoiced' LIMIT 1`).Scan(&issued))
	inv := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(99300).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetStatusID(issued).SetTitle("Contended application").SetLineItems(`[{"title":"Work","quantity":1,"unit_price":100}]`).SaveX(ctx)
	a := authorization.Actor{UserID: admin, Channel: "web"}
	ctx = authorization.WithActor(ctx, authorization.New(pool), a)
	svc := settlement.New(pool)
	actor := settlement.Actor{ID: admin, CompanyID: company}
	p, err := svc.RecordCustomerPayment(ctx, a, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 10000, Method: settlement.Cash, ReceivedDate: "2020-01-01"})
	historyCheck(t, err)
	var credit uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE source_payment_id=$1`, p.ID).Scan(&credit))
	holder, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer holder.Rollback(context.Background())
	var blocker int32
	historyCheck(t, holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(100300)`).Scan(&blocker))
	_, err = pool.Exec(ctx, `CREATE FUNCTION hold_financial_refund() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100300); RETURN NEW; END $$; CREATE TRIGGER hold_financial_refund BEFORE INSERT ON credit_refunds FOR EACH ROW EXECUTE FUNCTION hold_financial_refund()`)
	historyCheck(t, err)
	refund := make(chan error, 1)
	application := make(chan error, 1)
	go func() {
		_, err := svc.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, CustomerID: c.ID, AmountCents: 8000, Method: settlement.Cash, EffectiveDate: "2020-01-02", Reason: "Competing consumption"})
		refund <- err
	}()
	writer := timeExportDMLWaiter(t, ctx, pool, blocker, 100300, "INSERT INTO credit_refunds")
	go func() {
		_, err := svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: inv.ID, CreditID: credit, RequestedCents: 3000, EffectiveDate: "2020-01-02"})
		application <- err
	}()
	waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
	historyCheck(t, holder.Commit(ctx))
	historyCheck(t, <-refund)
	if err := <-application; !errors.Is(err, settlement.ErrInvalidAmount) {
		t.Fatalf("oversubscribed source application=%v", err)
	}
	var applied, refunded int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce((SELECT sum(amount_cents) FROM credit_applications WHERE credit_id=$1),0),coalesce((SELECT sum(amount_cents) FROM credit_refund_allocations WHERE credit_id=$1),0)`, credit).Scan(&applied, &refunded))
	if applied != 0 || refunded != 8000 {
		t.Fatalf("source overspent or losing command leaked: applied=%d refunded=%d", applied, refunded)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := financialAcceptancePost(t, web, cookie, "/reports/payments-credits/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	_, err = exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	credits, cash := 0, 0
	for _, r := range timeExportCSV(t, requestBody(t, web, cookie, "GET", path+"/download", 200)) {
		if r["row_type"] == "credit_source" {
			credits++
			if r["credit_id"] != credit.String() || r["current_available_amount"] != "20.00" {
				t.Fatalf("source conservation=%v", r)
			}
		}
		if r["row_type"] == "net_payments_total" && r["section"] == "dated_history" {
			cash++
			if r["cash_effect"] != "20.00" {
				t.Fatalf("cash reconciliation=%v", r)
			}
		}
		if r["row_type"] == "event" && r["event_type"] == "credit_application" {
			t.Fatal("losing command entered immutable report history")
		}
	}
	if credits != 1 || cash != 1 {
		t.Fatal("missing contention reconciliation")
	}
}
