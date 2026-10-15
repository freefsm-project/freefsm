package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"net/url"
	"testing"
)

func TestCommercialFinancialReviewedJobCustomerReplacement(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	old := client.Customer.Create().SetCompanyID(company).SetDisplayName("Old monetary Customer").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Exempt reviewed Customer").SetTaxExempt(true).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(old.ID).SetJobType("Reviewed monetary replacement").SaveX(ctx)
	lines := `[{"title":"One","quantity":1,"unit_price":0.05,"taxable":true},{"title":"Two","quantity":1,"unit_price":0.05,"taxable":true}]`
	legacy := client.Estimate.Create().SetCompanyID(company).SetCustomerID(old.ID).SetJobID(j.ID).SetTitle("Legacy monetary anchor").SetTerms("Recorded").SetTaxRate("10").SetLineItems(lines).SaveX(ctx)
	for _, kind := range []string{"estimate", "invoice"} {
		documentTransactionRequest(t, router, cookie, "POST", "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(old.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"New-rule monetary anchor"}, "invoice_date": {"2026-10-15"}, "tax_rate": {"10"}, "line_items": {lines}}, 303)
	}
	path := fmt.Sprintf("/jobs/%d", j.ID)
	values := url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(next.ID)}, "job_type": {j.JobType}, "replace_customer": {"true"}}
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	rows, err := pool.Query(ctx, `SELECT customer_id,financial FROM estimates UNION ALL SELECT customer_id,financial FROM invoices`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var customer int64
		var raw string
		if err := rows.Scan(&customer, &raw); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		s, err := commercial.Read(raw)
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if customer != next.ID || !s.Input.TaxExempt || s.Result.TotalCents != 10 || s.Result.TaxCents != 0 {
			rows.Close()
			t.Fatalf("reassignment did not atomically accept exemption: %d %+v", customer, s)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	var audited bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE action='job.edit' AND target_type='estimate' AND target_id=$1 AND detail->'before'->>'financial'='' AND (detail->'after'->>'financial')::jsonb->'result'->>'total_cents'='10')`, legacy.ID).Scan(&audited); err != nil {
		t.Fatal(err)
	}
	if !audited {
		t.Fatal("Customer replacement omitted monetary before/after audit")
	}
	// Removing exemption would make this signed-line document negative after
	// tax. The Job, earlier recalculated documents and their audits all roll back.
	documentTransactionRequest(t, router, cookie, "POST", "/estimates", url.Values{"customer_id": {fmt.Sprint(next.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Signed tax blocker"}, "tax_rate": {"10"}, "line_items": {`[{"title":"Untaxed","quantity":1,"unit_price":0.10},{"title":"Taxable correction","quantity":1,"unit_price":-0.10,"taxable":true}]`}}, 303)
	values.Set("customer_id", fmt.Sprint(old.ID))
	values = commercialReviewedValues(t, router, cookie, path, values)
	before := documentTransactionSnapshot(t, ctx, pool)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 409)
	if after := documentTransactionSnapshot(t, ctx, pool); before != after {
		t.Fatal("invalid reassignment partially published financial values or audits")
	}
	if current := client.Job.GetX(ctx, j.ID); current.CustomerID != next.ID {
		t.Fatal("invalid reassignment changed the Job Customer")
	}
}
