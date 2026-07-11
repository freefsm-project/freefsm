package templates

import (
	"bytes"
	"context"
	"math"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestLineItemHelpersMatchCanonicalMoney(t *testing.T) {
	items := []services.LineItem{
		{Title: "Fractional taxable", UnitPrice: 10.25, Quantity: 1.5, Discount: 0.40, Surcharge: 0.15, Taxable: true},
		{Title: "Exempt surcharge", UnitPrice: 4.20, Quantity: 2, Discount: 0.10, Surcharge: 0.35},
	}

	line, err := services.LineAmount(items[0])
	if err != nil {
		t.Fatal(err)
	}
	totals, err := services.CalculateTotals(items, "8.25")
	if err != nil {
		t.Fatal(err)
	}

	assertCents := func(name string, got float64, want int64) {
		t.Helper()
		if cents := int64(math.Round(got * 100)); cents != want {
			t.Errorf("%s = %d cents, want %d", name, cents, want)
		}
	}
	assertCents("lineItemTotal", lineItemTotal(items[0]), line.MinorUnits())
	assertCents("lineItemsTotal", lineItemsTotal(items), totals.Subtotal.MinorUnits())
	assertCents("taxAmount", taxAmount(items, "8.25"), totals.Tax.MinorUnits())
	assertCents("grandTotal", grandTotal(items, "8.25"), totals.Total.MinorUnits())
}

func TestLineItemHelpersUseVisibleZeroFallbackForCorruptValues(t *testing.T) {
	invalid := services.LineItem{Title: "Corrupt", Quantity: math.NaN(), UnitPrice: 10, Taxable: true}
	items := []services.LineItem{invalid}

	if got := lineItemTotal(invalid); got != 0 {
		t.Errorf("lineItemTotal() = %v, want 0", got)
	}
	if got := lineItemsTotal(items); got != 0 {
		t.Errorf("lineItemsTotal() = %v, want 0", got)
	}
	if got := taxAmount(items, "8.25"); got != 0 {
		t.Errorf("taxAmount() = %v, want 0", got)
	}
	if got := grandTotal(items, "invalid"); got != 0 {
		t.Errorf("grandTotal() = %v, want 0", got)
	}

	html := renderComponent(t, EstimateShow(EstimateDetail{TaxRate: "8.25", LineItems: items}))
	if !strings.Contains(html, "$0.00") {
		t.Error("rendered corrupt line item does not show the $0.00 fallback")
	}
}

func TestShowPagesRenderCompactQuantitiesAndCanonicalTotals(t *testing.T) {
	items := []services.LineItem{
		{Title: "Whole", UnitPrice: 2, Quantity: 1},
		{Title: "Fractional taxable", UnitPrice: 10.25, Quantity: 1.5, Discount: 0.40, Surcharge: 0.15, Taxable: true},
	}

	tests := []struct {
		name      string
		component templ.Component
		amounts   []string
	}{
		{"job", JobShow(JobDetail{LineItems: items}), []string{"$2.00", "$15.13"}},
		{"estimate", EstimateShow(EstimateDetail{TaxRate: "8.25", LineItems: items}), []string{"Subtotal: $17.13", "Tax: $1.25", "Grand Total: $18.38"}},
		{"invoice", InvoiceShow(InvoiceDetail{TaxRate: "8.25", LineItems: items}), []string{"Subtotal: $17.13", "Tax: $1.25", "Grand Total: $18.38"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderComponent(t, tt.component)
			for _, want := range append([]string{">1<", ">1.5<"}, tt.amounts...) {
				if !strings.Contains(html, want) {
					t.Errorf("rendered page does not contain %q", want)
				}
			}
		})
	}
}

func TestShowPagesKeepTaxableLabelWhenTaxRoundsToZero(t *testing.T) {
	items := []services.LineItem{{Title: "Small taxable item", UnitPrice: 0.01, Quantity: 1, Taxable: true}}
	html := renderComponent(t, EstimateShow(EstimateDetail{TaxRate: "0.5", LineItems: items}))
	if !strings.Contains(html, "0.5") || strings.Contains(html, ">No<") {
		t.Fatalf("taxable line lost its tax rate label: %s", html)
	}
}

func renderComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{Role: "admin"})
	var output bytes.Buffer
	if err := component.Render(ctx, &output); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return output.String()
}
