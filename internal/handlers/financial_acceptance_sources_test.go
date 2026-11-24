package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
)

func TestFinancialAcceptanceSourcesPartialFullUnwindAndFIFO(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Source conservation").SaveX(ctx)
	var issued int64
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='invoice:invoiced' LIMIT 1`).Scan(&issued))
	historyCheck(t, pool.QueryRow(ctx, `SELECT (now() AT TIME ZONE timezone)::date::text FROM company_settings WHERE company_id=$1`, company).Scan(&today))
	inv := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(99100).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetTitle("Source application target").SetStatusID(issued).SetLineItems(`[{"title":"Work","quantity":1,"unit_price":100}]`).SaveX(ctx)
	ctx = authorization.WithActor(ctx, authorization.New(pool), authorization.Actor{UserID: admin, Channel: "web"})
	svc := settlement.New(pool)
	actor := settlement.Actor{ID: admin, CompanyID: company}
	sources := []uuid.UUID{}
	payments := []uuid.UUID{}
	// Entry order deliberately opposes source effective-date FIFO order.
	for _, p := range []struct {
		amount int64
		date   settlement.Date
	}{{7000, "2020-02-01"}, {3000, "2020-01-01"}} {
		r, err := svc.RecordCustomerPayment(ctx, authorization.Actor{UserID: admin, Channel: "web"}, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: p.amount, Method: settlement.Cash, ReceivedDate: p.date})
		historyCheck(t, err)
		var credit uuid.UUID
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE source_payment_id=$1`, r.ID).Scan(&credit))
		sources = append(sources, credit)
		payments = append(payments, r.ID)
	}
	check := func(due, available string) {
		t.Helper()
		for _, selection := range []struct{ route, row, key, want string }{{"/reports/invoice-aging/export", "grand_total", "outstanding_amount", due}, {"/reports/credits/export", "current_credit_total", "current_available_amount", available}} {
			path := financialAcceptancePost(t, web, cookie, selection.route, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "operation_id": {newOperationKey()}}, 303)
			_, err := exports.Product(pool).ProcessOne(ctx)
			historyCheck(t, err)
			found := false
			for _, row := range timeExportCSV(t, requestBody(t, web, cookie, "GET", path+"/download", 200)) {
				if row["row_type"] == selection.row {
					found = true
					if row[selection.key] != selection.want {
						t.Fatalf("%s=%s want %s", selection.key, row[selection.key], selection.want)
					}
				}
			}
			if !found {
				t.Fatalf("missing %s", selection.row)
			}
		}
	}
	check("100.00", "100.00")
	first, err := svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: inv.ID, CreditID: sources[1], RequestedCents: 3000, EffectiveDate: settlement.Date(today)})
	historyCheck(t, err)
	check("70.00", "70.00")
	second, err := svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: inv.ID, CreditID: sources[0], RequestedCents: 7000, EffectiveDate: settlement.Date(today)})
	historyCheck(t, err)
	check("0.00", "0.00")
	for _, app := range []uuid.UUID{first.ID, second.ID} {
		_, err := svc.ReverseCreditApplication(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: app, InvoiceID: inv.ID, EffectiveDate: settlement.Date(today), Reason: "Unwind application"})
		historyCheck(t, err)
	}
	check("100.00", "100.00")
	refund, err := svc.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, CustomerID: c.ID, AmountCents: 5000, Method: settlement.Cash, EffectiveDate: settlement.Date(today), Reason: "Multi source FIFO"})
	historyCheck(t, err)
	check("100.00", "50.00")
	path := financialAcceptancePost(t, web, cookie, "/reports/settlements/export", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "operation_id": {newOperationKey()}}, 303)
	_, err = exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	rows := timeExportCSV(t, requestBody(t, web, cookie, "GET", path+"/download", 200))
	allocations := []map[string]string{}
	events := map[string]int{}
	for _, r := range rows {
		if r["row_type"] == "event" {
			events[r["event_type"]]++
		}
		if r["row_type"] == "allocation" && r["event_id"] == refund.ID.String() {
			allocations = append(allocations, r)
			if r["cash_effect"] != "" || r["event_amount"] != "" || r["allocation_id"] != refund.ID.String() {
				t.Fatalf("additive/invented source row: %v", r)
			}
		}
		if r["row_type"] == "net_payments_total" && r["section"] == "dated_history" && r["cash_effect"] != "50.00" {
			t.Fatalf("cash multiplied by joins: %v", r)
		}
	}
	if len(allocations) != 2 || allocations[0]["credit_id"] != sources[1].String() || allocations[0]["allocation_amount"] != "30.00" || allocations[1]["credit_id"] != sources[0].String() || allocations[1]["allocation_amount"] != "20.00" {
		t.Fatalf("persisted FIFO portions=%v", allocations)
	}
	if events["credit_application"] != 2 || events["application_reversal"] != 2 {
		t.Fatalf("lost immutable applications=%v", events)
	}
	_, err = svc.ReverseRefund(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: refund.ID, CustomerID: c.ID, EffectiveDate: settlement.Date(today), Reason: "Unwind refund"})
	historyCheck(t, err)
	for _, payment := range payments {
		_, err := svc.ReverseUnappliedPayment(ctx, authorization.Actor{UserID: admin, Channel: "web"}, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: payment, CustomerID: c.ID, EffectiveDate: settlement.Date(today), Reason: "error"})
		historyCheck(t, err)
	}
	check("100.00", "0.00")
}
