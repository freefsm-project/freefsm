package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/google/uuid"
)

func TestFinancialAcceptanceUnknownRecordingTimesAndArchivedHistoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained unknown source").SaveX(ctx)
	payment, credit := uuid.NewString(), uuid.NewString()
	// Insert preserved legacy evidence directly, without disabling immutability,
	// overwriting commands' real timestamps, or claiming an upgrade was tested.
	_, err := pool.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,amount_cents,method,received_date,created_at) VALUES($1,$2,$3,1234,'cash','2020-01-01',NULL)`, payment, company, c.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date) VALUES($1,$2,$3,$4,1234,'2020-01-01')`, credit, company, c.ID, payment)
	historyCheck(t, err)
	for _, archived := range []bool{false, true} {
		if archived {
			// Archiving must satisfy the real zero-Credit invariant; retain the
			// unknown receipt and its source rather than bypassing the guard.
			commandCtx := authorization.WithActor(ctx, authorization.New(pool), authorization.Actor{UserID: admin, Channel: "web"})
			_, err = settlement.New(pool).RefundCredit(commandCtx, settlement.Actor{ID: admin, CompanyID: company}, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, CustomerID: c.ID, AmountCents: 1234, Method: settlement.Cash, EffectiveDate: "2020-01-02", Reason: "Close balance before archive"})
			historyCheck(t, err)
			_, err = pool.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, c.ID)
			historyCheck(t, err)
		}
		values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "from": {"2026-03-08"}, "to": {"2026-03-08"}, "include_archived": {fmt.Sprint(archived)}}
		body := requestBody(t, web, cookie, "GET", "/reports/payments-credits?"+values.Encode(), 200)
		assertContains(t, body, "Unknown recording-time history")
		assertContains(t, body, "Retained unknown source")
		assertContains(t, body, "$12.34")
		if archived {
			assertContains(t, body, "read-only")
		}
		values.Set("operation_id", newOperationKey())
		path := financialAcceptancePost(t, web, cookie, "/reports/payments-credits/export", values, 303)
		_, err = exports.Product(pool).ProcessOne(ctx)
		historyCheck(t, err)
		unknown, current, period := 0, false, false
		for _, r := range timeExportCSV(t, requestBody(t, web, cookie, "GET", path+"/download", 200)) {
			if r["row_type"] == "event" {
				unknown++
				if r["section"] != "unknown_entry_time" || r["entered_at"] != "" || r["entry_time_known"] != "false" {
					t.Fatalf("invented historical entry: %v", r)
				}
			}
			if r["row_type"] == "net_payments_total" && r["section"] == "period" {
				period = true
				if r["cash_effect"] != "0.00" {
					t.Fatalf("undated cash entered dated total: %v", r)
				}
			}
			if r["row_type"] == "credit_source" {
				current = true
				if r["source_payment_id"] != payment || r["source_entered_at"] != "" || r["source_entry_time_known"] != "false" || r["current_available_amount"] != "12.34" {
					t.Fatalf("invented Credit provenance: %v", r)
				}
			}
		}
		if unknown != 2 || current == archived || !period {
			t.Fatalf("unknown/current/period=%d/%v/%v", unknown, current, period)
		}
	}
	var stillArchived bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM customers WHERE id=$1`, c.ID).Scan(&stillArchived))
	if !stillArchived {
		t.Fatal("report reopened archived Customer")
	}
}
