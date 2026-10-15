package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"net/url"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const adjustedGoldenLines = `[{"title":"Fractional work","quantity":1.5,"unit_price":10.01,"quantity_decimal":"1.5","unit_price_decimal":"10.01","taxable":true,"discount_adjustment":{"kind":"percentage","value":"10"},"surcharge_adjustment":{"kind":"fixed","value":"0.05"}},{"title":"Untaxed work","quantity":1,"unit_price":5},{"title":"Signed correction","quantity":1,"unit_price":-0.50,"taxable":true}]`

func TestCommercialFinancialHTTPPDFSettlementAgreement(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Golden surface customer").SaveX(ctx)
			values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Golden amounts"}, "invoice_date": {"2026-10-15"}, "line_items": {adjustedGoldenLines}, "tax_rate": {"8.25"}, "adjustments": {`{"discount":{"kind":"fixed","value":"1.00"},"surcharge":{"kind":"percentage","value":"10"}}`}}
			documentTransactionRequest(t, router, cookie, "POST", "/"+kind+"s", values, 303)
			var id int64
			var raw string
			if err := pool.QueryRow(ctx, `SELECT id,financial FROM `+kind+`s`).Scan(&id, &raw); err != nil {
				t.Fatal(err)
			}
			snapshot, err := commercial.Read(raw)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.Result.PretaxCents != 1883 || snapshot.Result.TaxCents != 113 || snapshot.Result.TotalCents != 1996 {
				t.Fatalf("accepted golden amounts=%+v", snapshot.Result)
			}
			path := fmt.Sprintf("/%ss/%d", kind, id)
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "$19.96")
			fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
			pdf := requestBody(t, router, cookie, "GET", path+"/pdf", 200)
			command := exec.Command("pdftotext", "-", "-")
			command.Stdin = strings.NewReader(pdf)
			text, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("PDF: %v %s", err, text)
			}
			assertContains(t, string(text), "$19.96")
			assertContains(t, string(text), "$1.13")
			var source string
			if err := pool.QueryRow(ctx, `SELECT source_snapshot->'document'->>'financial' FROM document_pdf_versions WHERE document_type=$1 AND document_id=$2`, kind, id).Scan(&source); err != nil {
				t.Fatal(err)
			}
			if source != raw {
				t.Fatal("PDF source did not capture accepted financial revision")
			}
			if kind == "invoice" {
				view, err := settlement.New(pool).InvoiceSettlement(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, id)
				if err != nil {
					t.Fatal(err)
				}
				if view.TotalCents != 1996 || view.AmountDueCents != 1996 {
					t.Fatalf("settlement disagrees: %+v", view)
				}
			}
		})
	}
}

func TestCommercialLegacyMetadataPreservesRecordedArithmeticHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Historical customer").SaveX(ctx)
	lines := `[{"title":"One","quantity":1,"unit_price":0.05,"taxable":true},{"title":"Two","quantity":1,"unit_price":0.05,"taxable":true}]`
	inv := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Historical invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetTerms("Recorded").SetTaxRate("10").SetLineItems(lines).SaveX(ctx)
	inv = client.Invoice.GetX(ctx, inv.ID) // Include the database-issued number.
	path := fmt.Sprintf("/invoices/%d", inv.ID)
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
	original := requestBody(t, router, cookie, "GET", path+"/pdf", 200)
	var version int64
	if err := pool.QueryRow(ctx, `SELECT id FROM document_pdf_versions`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "invoice_number": {fmt.Sprint(inv.InvoiceNumber)}, "title": {"Historical invoice"}, "invoice_date": {inv.InvoiceDate.Format("2006-01-02")}, "due_date": {inv.DueDate.Format("2006-01-02")}, "notes": {"Metadata edit"}, "tax_rate": {"10"}, "line_items": {lines}, "adjustments": {"{}"}}
	values.Set("line_items", strings.ReplaceAll(lines, `"quantity":1`, `"quantity":1.00`))
	values.Set("tax_rate", "10.0")
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	var raw string
	if err := pool.QueryRow(ctx, `SELECT financial FROM invoices WHERE id=$1`, inv.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != "" {
		t.Fatal("metadata save transitioned historical arithmetic")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "$0.11")
	values.Set("line_items", strings.Replace(lines, "0.05", "0.06", 1))
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "$0.13")
	if old := requestBody(t, router, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d&confirm_stale=1", path, version), 200); old != original {
		t.Fatal("reviewed monetary edit changed immutable legacy PDF bytes")
	}
}
