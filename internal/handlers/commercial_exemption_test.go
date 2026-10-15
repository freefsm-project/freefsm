package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"net/url"
	"strings"
	"testing"
)

func TestCommercialFinancialExemptionCapturedAtMonetaryCommit(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Exemption customer").SaveX(ctx)
	lines := `[{"title":"One","quantity":1,"unit_price":0.05,"taxable":true},{"title":"Two","quantity":1,"unit_price":0.05,"taxable":true}]`
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Captured exemption"}, "tax_rate": {"10"}, "line_items": {lines}}
	documentTransactionRequest(t, router, cookie, "POST", "/estimates", values, 303)
	var id int64
	var before, after string
	if err := pool.QueryRow(ctx, `SELECT id,financial FROM estimates`).Scan(&id, &before); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/estimates/%d", id)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "$0.12")
	client.Customer.UpdateOneID(c.ID).SetTaxExempt(true).SaveX(ctx)
	values.Set("notes", "Preserve accepted amounts")
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	if err := pool.QueryRow(ctx, `SELECT financial FROM estimates WHERE id=$1`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("metadata save silently applied new Customer exemption")
	}
	values.Set("line_items", strings.Replace(lines, `"quantity":1`, `"quantity":2`, 1))
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "$0.15")
	if err := pool.QueryRow(ctx, `SELECT financial FROM estimates WHERE id=$1`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	snapshot, err := commercial.Read(after)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Input.TaxExempt || snapshot.Result.TaxCents != 0 {
		t.Fatalf("commit did not capture exemption: %+v", snapshot)
	}
}
