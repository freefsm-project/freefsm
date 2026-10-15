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
	htmlnode "golang.org/x/net/html"
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

func TestLegacyLineItemHelpersRetainZeroFallback(t *testing.T) {
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

}

func TestCommercialCorruptValuesRenderUnavailable(t *testing.T) {
	for _, bad := range []struct {
		name string
		line services.LineItem
	}{
		{"quantity", services.LineItem{Title: "Corrupt", Description: "Retain for review", Quantity: math.NaN(), UnitPrice: 10, Taxable: true}},
		{"price", services.LineItem{Title: "Corrupt", Description: "Retain for review", Quantity: 1, UnitPrice: math.Inf(1), Taxable: true}},
	} {
		items := []services.LineItem{bad.line, {Title: "Valid neighbor", Quantity: 1, UnitPrice: 12.34}}
		for _, kind := range []string{"estimate", "invoice"} {
			t.Run(kind+"/"+bad.name, func(t *testing.T) {
				var component templ.Component = EstimateShow(EstimateDetail{TaxRate: "8.25", LineItems: items})
				if kind == "invoice" {
					component = InvoiceShow(InvoiceDetail{TaxRate: "8.25", LineItems: items})
				}
				doc := parseLineItemHTML(t, renderPricingComponent(t, component))
				text := lineItemHTMLText(doc)
				for _, want := range []string{"Corrupt", "Retain for review", "Valid neighbor", "Subtotal: Unavailable", "Tax: Unavailable", "Grand Total: Unavailable"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing graceful rendering %q: %s", want, text)
					}
				}
				for _, badValue := range []string{"$0.00", "$Unavailable", "NaN", "+Inf", "-Inf"} {
					if strings.Contains(text, badValue) {
						t.Errorf("corrupt data was presented as a financial value %q", badValue)
					}
				}
				rows := lineItemHTMLRows(doc)
				if row := rows["Corrupt"]; len(row) != 6 || row[5] != "Unavailable" {
					t.Errorf("corrupt line amount: %v", row)
				}
				if row := rows["Valid neighbor"]; len(row) != 6 || row[5] != "$12.34" {
					t.Errorf("valid neighboring line lost its amount: %v", row)
				}
			})
		}
	}
	// A legitimate zero is still money, not an unavailable/error sentinel.
	zero := []services.LineItem{{Title: "Legitimate zero", Quantity: 1, UnitPrice: 0}}
	for _, component := range []templ.Component{EstimateShow(EstimateDetail{TaxRate: "0", LineItems: zero}), InvoiceShow(InvoiceDetail{TaxRate: "0", LineItems: zero})} {
		text := lineItemHTMLText(parseLineItemHTML(t, renderPricingComponent(t, component)))
		if !strings.Contains(text, "Grand Total: $0.00") || strings.Contains(text, "Unavailable") {
			t.Fatalf("legitimate zero was lost: %s", text)
		}
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
			html := renderPricingComponent(t, tt.component)
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
	html := renderPricingComponent(t, EstimateShow(EstimateDetail{TaxRate: "0.5", LineItems: items}))
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
		{"estimate", EstimateForm(EstimateFormPageData{IsNew: true, Estimate: &EstimateDetail{}, ItemsJSON: data.ItemsJSON, ExistingItemsJSON: data.ExistingItemsJSON, CustomersJSON: data.CustomersJSON})},
		{"invoice", InvoiceForm(InvoiceFormPageData{IsNew: true, Invoice: &InvoiceDetail{}, ItemsJSON: data.ItemsJSON, ExistingItemsJSON: data.ExistingItemsJSON, CustomersJSON: data.CustomersJSON})},
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
			// Decimal text entry must not impose the old six-decimal floor or
			// native floating-point input coercion. Positivity is validated at save.
			var quantity map[string]string
			var visit func(*htmlnode.Node)
			visit = func(node *htmlnode.Node) {
				if node.Type == htmlnode.ElementNode && node.Data == "input" {
					attrs := map[string]string{}
					for _, a := range node.Attr {
						attrs[a.Key] = a.Val
					}
					if attrs["x-model"] == "item.quantity" {
						quantity = attrs
					}
				}
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					visit(child)
				}
			}
			visit(parseLineItemHTML(t, html))
			if quantity["type"] != "text" || quantity["inputmode"] != "decimal" || quantity["aria-label"] != "Quantity" {
				t.Errorf("quantity is not exact decimal entry: %v", quantity)
			}
			if quantity["min"] == "0.000001" {
				t.Error("obsolete six-decimal quantity floor returned")
			}
		})
	}
}

func parseLineItemHTML(t *testing.T, markup string) *htmlnode.Node {
	t.Helper()
	doc, err := htmlnode.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func lineItemHTMLText(node *htmlnode.Node) string {
	var text strings.Builder
	var visit func(*htmlnode.Node)
	visit = func(n *htmlnode.Node) {
		if n.Type == htmlnode.ElementNode && (n.Data == "script" || n.Data == "style" || n.Data == "template") {
			return
		}
		if n.Type == htmlnode.TextNode {
			text.WriteString(n.Data)
			text.WriteByte(' ')
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(node)
	return strings.Join(strings.Fields(text.String()), " ")
}

func lineItemHTMLRows(doc *htmlnode.Node) map[string][]string {
	rows := map[string][]string{}
	var visit func(*htmlnode.Node)
	visit = func(n *htmlnode.Node) {
		if n.Type == htmlnode.ElementNode && n.Data == "tr" {
			var cells []string
			for child := n.FirstChild; child != nil; child = child.NextSibling {
				if child.Type == htmlnode.ElementNode && child.Data == "td" {
					cells = append(cells, lineItemHTMLText(child))
				}
			}
			if len(cells) > 0 {
				rows[cells[0]] = cells
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(doc)
	return rows
}

func TestLineItemEditorCreateItemPermission(t *testing.T) {
	component := LineItemEditor(LineItemEditorData{ItemsJSON: `[]`, ExistingItemsJSON: `[]`, CustomersJSON: `{}`})
	for _, role := range []string{"admin", "dispatcher"} {
		if html := renderComponentForRole(t, component, role); strings.Contains(html, "Create Item") || strings.Contains(html, "item-create-dialog") {
			t.Errorf("legacy role %q received inline Create Item controls without explicit grants", role)
		}
	}
	if html := renderComponentForRole(t, component, "technician"); strings.Contains(html, "Create Item") || strings.Contains(html, "item-create-dialog") {
		t.Error("technician received inline Create Item controls")
	}
}

func renderComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	return renderComponentForUser(t, component, &middleware.UserInfo{Role: "admin", ItemPermissions: map[string]bool{"item.create": true, "pricebook.create": true}})
}

func renderPricingComponent(t *testing.T, component templ.Component) string {
	t.Helper()
	var output bytes.Buffer
	if err := component.Render(WithDetailAccess(context.Background(), DetailAccess{"pricing": true}), &output); err != nil {
		t.Fatal(err)
	}
	return output.String()
}

func renderComponentForRole(t *testing.T, component templ.Component, role string) string {
	t.Helper()
	return renderComponentForUser(t, component, &middleware.UserInfo{Role: role})
}

func renderComponentForUser(t *testing.T, component templ.Component, user *middleware.UserInfo) string {
	t.Helper()
	ctx := context.WithValue(context.Background(), middleware.UserKey, user)
	var output bytes.Buffer
	if err := component.Render(ctx, &output); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return output.String()
}
