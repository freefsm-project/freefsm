package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestFinancialAcceptanceAllRowsExactTotalsAndSpreadsheetEscaping(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	name := "=unsafe,\"customer\"\nsecond line"
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName(name).SaveX(ctx)
	var issued int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='invoice:invoiced' LIMIT 1`).Scan(&issued))
	invoiceIDs := []string{}
	for i := 1; i <= 105; i++ {
		inv := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(int64(i)).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetStatusID(issued).SetTitle("All matching rows").SetLineItems(`[{"title":"Cent precision","quantity":1,"unit_price":1.01}]`).SaveX(ctx)
		invoiceIDs = append(invoiceIDs, fmt.Sprint(inv.ID))
	}
	path := financialAcceptancePost(t, web, cookie, "/reports/invoice-aging/export", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "operation_id": {newOperationKey()}}, 303)
	_, err := exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	rows := timeExportCSV(t, requestBody(t, web, cookie, "GET", path+"/download", 200))
	invoices, totals := 0, 0
	for _, r := range rows {
		if r["as_of"] != rows[0]["as_of"] || r["timezone"] != rows[0]["timezone"] {
			t.Fatal("inconsistent snapshot metadata")
		}
		if r["row_type"] == "invoice" {
			invoices++
			if r["invoice_id"] != invoiceIDs[invoices-1] || r["customer"] != "'"+name || r["outstanding_amount"] != "1.01" {
				t.Fatalf("sorted escaped row=%v", r)
			}
		}
		if r["row_type"] == "grand_total" {
			totals++
			if r["invoice_count"] != "105" || r["outstanding_amount"] != "106.05" {
				t.Fatalf("all matching totals=%v", r)
			}
		}
	}
	if invoices != 105 || totals != 1 {
		t.Fatalf("page-only/missing totals: invoices=%d totals=%d", invoices, totals)
	}
}
