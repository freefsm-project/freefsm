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

func TestDocumentFormsRenderSharedLineItemEditorContract(t *testing.T) {
	data := LineItemEditorData{
		ItemsJSON:         `[{"id":7,"name":"Catalog title","description":"Snapshot","unit_price":12.5,"taxable":true,"tax_rate":"8.25"}]`,
		ExistingItemsJSON: `[{"item_id":0,"title":"Free text","description":"Frozen","unit_price":3.5,"quantity":1.25,"taxable":true,"tax_rate":"4","discount":0.5,"surcharge":0.25}]`,
		CustomersJSON:     `{"default_tax_rate":"8.25","customers":{}}`,
	}
	tests := []struct {
		name      string
		component templ.Component
	}{
		{"estimate", EstimateForm(EstimateFormPageData{Estimate: &EstimateDetail{}, ItemsJSON: data.ItemsJSON, ExistingItemsJSON: data.ExistingItemsJSON, CustomersJSON: data.CustomersJSON})},
		{"invoice", InvoiceForm(InvoiceFormPageData{Invoice: &InvoiceDetail{}, ItemsJSON: data.ItemsJSON, ExistingItemsJSON: data.ExistingItemsJSON, CustomersJSON: data.CustomersJSON})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			html := renderComponentForRole(t, tt.component, "admin")
			for _, want := range []string{
				`data-line-item-editor`,
				`x-model="item.title"`,
				`x-model="item.discount"`,
				`x-model="item.surcharge"`,
				`Free-text line`,
				`min="0.000001"`,
				`name="line_items"`,
			} {
				if !strings.Contains(html, want) {
					t.Errorf("rendered form does not contain %q", want)
				}
			}
			for _, forbidden := range []string{`id="items-catalog"`, `f.name === i.title`, `x-model="item.tax_rate"`} {
				if strings.Contains(html, forbidden) {
					t.Errorf("rendered form contains obsolete editor behavior %q", forbidden)
				}
			}
		})
	}
}

func TestLineItemEditorCreateItemPermission(t *testing.T) {
	component := LineItemEditor(LineItemEditorData{ItemsJSON: `[]`, ExistingItemsJSON: `[]`, CustomersJSON: `{}`})
	for _, role := range []string{"admin", "dispatcher"} {
		if html := renderComponentForRole(t, component, role); !strings.Contains(html, "Create Item") || !strings.Contains(html, "item-create-dialog") {
			t.Errorf("role %q did not receive inline Create Item controls", role)
		}
	}
	if html := renderComponentForRole(t, component, "technician"); strings.Contains(html, "Create Item") || strings.Contains(html, "item-create-dialog") {
		t.Error("technician received inline Create Item controls")
	}
}

func renderComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	return renderComponentForRole(t, component, "admin")
}

func renderComponentForRole(t *testing.T, component templ.Component, role string) string {
	t.Helper()
	ctx := context.WithValue(context.Background(), middleware.UserKey, &middleware.UserInfo{Role: role})
	var output bytes.Buffer
	if err := component.Render(ctx, &output); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return output.String()
}
