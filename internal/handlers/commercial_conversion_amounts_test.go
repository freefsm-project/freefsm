package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"net/url"
	"strings"
	"testing"
)

func TestCommercialBillingAndConversionPreserveAcceptedArithmeticHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Billing arithmetic customer").SaveX(ctx)
	lines := `[{"title":"One","quantity":1,"unit_price":0.05,"taxable":true,"tax_rate":"0"},{"title":"Two","quantity":1,"unit_price":0.05,"taxable":true,"tax_rate":"0"}]`
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Billing work").SetLineItems(lines).SaveX(ctx)
	// Compare the durable capture, including database normalization/identity,
	// rather than the builder's pre-trigger JSON representation.
	j = client.Job.GetX(ctx, j.ID)
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET default_tax_rate='10' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, fmt.Sprintf("/jobs/%d/create-estimate", j.ID), url.Values{}, 303)
	var estimateID int64
	var financial string
	if err := pool.QueryRow(ctx, `SELECT id,financial FROM estimates`).Scan(&estimateID, &financial); err != nil {
		t.Fatal(err)
	}
	accepted, err := commercial.Read(financial)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Result.TotalCents != 12 {
		t.Fatalf("Job billing used old aggregate/document or Job captured tax: %+v", accepted.Result)
	}
	fieldPost(t, router, cookie, fmt.Sprintf("/jobs/%d/invoices", j.ID), url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Invoice billing"}, "invoice_date": {"2026-10-15"}, "tax_rate": {"10"}, "line_items": {lines}}, 303)
	var billed string
	if err := pool.QueryRow(ctx, `SELECT financial FROM invoices WHERE job_id=$1 AND estimate_id IS NULL`, j.ID).Scan(&billed); err != nil {
		t.Fatal(err)
	}
	invoiceAmounts, err := commercial.Read(billed)
	if err != nil {
		t.Fatal(err)
	}
	if invoiceAmounts.Result.TotalCents != 12 {
		t.Fatalf("Invoice Job billing disagrees: %+v", invoiceAmounts.Result)
	}
	// New company defaults and Customer exemption cannot reprice a conversion.
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET default_tax_rate='20' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	client.Customer.UpdateOneID(c.ID).SetTaxExempt(true).SaveX(ctx)
	source := fmt.Sprintf("/estimates/%d", estimateID)
	converted := fieldPost(t, router, cookie, source+"/convert", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	invoicePath := strings.Split(converted.Header().Get("Location"), "?")[0]
	assertContains(t, requestBody(t, router, cookie, "GET", invoicePath, 200), "$0.12")
	var copied string
	if err := pool.QueryRow(ctx, `SELECT financial FROM invoices WHERE estimate_id=$1 AND conversion_hidden_at IS NULL`, estimateID).Scan(&copied); err != nil {
		t.Fatal(err)
	}
	if copied != financial {
		t.Fatal("conversion repriced accepted monetary inputs/results")
	}
	fieldPost(t, router, cookie, invoicePath+"/revert-to-estimate", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	if err := pool.QueryRow(ctx, `SELECT financial FROM estimates WHERE id=$1`, estimateID).Scan(&copied); err != nil {
		t.Fatal(err)
	}
	if copied != financial {
		t.Fatal("reversion repriced accepted monetary inputs/results")
	}
	currentJob := client.Job.GetX(ctx, j.ID)
	if currentJob.LineItems != j.LineItems {
		t.Fatal("billing rewrote captured Job preview inputs")
	}
	// A genuinely historical source stays on legacy interpretation in both directions.
	old := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Historical conversion").SetTerms("Historical terms").SetTaxRate("10").SetLineItems(lines).SaveX(ctx)
	converted = fieldPost(t, router, cookie, fmt.Sprintf("/estimates/%d/convert", old.ID), url.Values{"idempotency_key": {newOperationKey()}}, 303)
	invoicePath = strings.Split(converted.Header().Get("Location"), "?")[0]
	assertContains(t, requestBody(t, router, cookie, "GET", invoicePath, 200), "$0.11")
	fieldPost(t, router, cookie, invoicePath+"/revert-to-estimate", url.Values{"idempotency_key": {newOperationKey()}}, 303)
	if err := pool.QueryRow(ctx, `SELECT financial FROM estimates WHERE id=$1`, old.ID).Scan(&copied); err != nil {
		t.Fatal(err)
	}
	if copied != "" {
		t.Fatal("historical conversion invented a monetary review")
	}
}
