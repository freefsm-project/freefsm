package services

import (
	"errors"
	"math"
	"testing"

	"github.com/freefsm-project/freefsm/internal/ent"
)

func TestDecodeLineItemsAcceptsIntegerAndFractionalQuantities(t *testing.T) {
	items, err := DecodeLineItems(`[
		{"title":"Whole","unit_price":10,"quantity":2},
		{"title":"Fractional","unit_price":10.25,"quantity":1.5}
	]`)
	if err != nil {
		t.Fatalf("DecodeLineItems() error = %v", err)
	}
	if got := items[0].Quantity; got != 2 {
		t.Errorf("integer quantity = %v, want 2", got)
	}
	if got := items[1].Quantity; got != 1.5 {
		t.Errorf("fractional quantity = %v, want 1.5", got)
	}
}

func TestServiceConsumersAgreeWithCanonicalTotals(t *testing.T) {
	items := []LineItem{
		{Title: "Taxable", UnitPrice: 10.25, Quantity: 1.5, Discount: 0.40, Surcharge: 0.15, Taxable: true},
		{Title: "Exempt", UnitPrice: 3, Quantity: 2.25, Discount: 0.10, Surcharge: 0.20},
	}
	encoded, err := EncodeLineItems(items)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := CalculateTotals(items, "8.25")
	if err != nil {
		t.Fatal(err)
	}
	want := canonical.Total.MajorUnits()
	if want != 23.23 {
		t.Fatalf("canonical total = %.2f, want 23.23", want)
	}

	estimateTotal, err := EstimateTotal(items, "8.25")
	if err != nil || estimateTotal != want {
		t.Errorf("EstimateTotal() = (%.2f, %v), want (%.2f, nil)", estimateTotal, err, want)
	}
	invoiceTotal, err := InvoiceTotal(items, "8.25")
	if err != nil || invoiceTotal != want {
		t.Errorf("InvoiceTotal() = (%.2f, %v), want (%.2f, nil)", invoiceTotal, err, want)
	}

	dashboard := &DashboardService{}
	invoice := &ent.Invoice{LineItems: encoded, TaxRate: "8.25"}
	if got := dashboard.invoiceSubtotal(invoice); got != canonical.Subtotal.MajorUnits() {
		t.Errorf("dashboard invoice subtotal = %.2f, want %.2f", got, canonical.Subtotal.MajorUnits())
	}
	if got := dashboard.invoiceTotal(invoice); got != want {
		t.Errorf("dashboard invoice total = %.2f, want %.2f", got, want)
	}
	if got := dashboard.estimateTotal(&ent.Estimate{LineItems: encoded, TaxRate: "8.25"}); got != want {
		t.Errorf("dashboard estimate total = %.2f, want %.2f", got, want)
	}

}

func TestValidateLineItemsRejectsInvalidDomainValues(t *testing.T) {
	tests := []struct {
		name string
		item LineItem
	}{
		{"blank title", LineItem{Title: "  ", Quantity: 1}},
		{"zero quantity", LineItem{Title: "x", Quantity: 0}},
		{"negative quantity", LineItem{Title: "x", Quantity: -1}},
		{"non-finite quantity", LineItem{Title: "x", Quantity: math.Inf(1)}},
		{"negative unit price", LineItem{Title: "x", Quantity: 1, UnitPrice: -1}},
		{"non-finite unit price", LineItem{Title: "x", Quantity: 1, UnitPrice: math.NaN()}},
		{"negative discount", LineItem{Title: "x", Quantity: 1, Discount: -1}},
		{"negative surcharge", LineItem{Title: "x", Quantity: 1, Surcharge: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateLineItems([]LineItem{tt.item}); !errors.Is(err, ErrInvalidLineItem) {
				t.Fatalf("ValidateLineItems() error = %v, want ErrInvalidLineItem", err)
			}
		})
	}

	valid := LineItem{Title: "Labor", Quantity: 0.25, UnitPrice: 10, Discount: 1, Surcharge: 2}
	if err := ValidateLineItems([]LineItem{valid}); err != nil {
		t.Fatalf("valid fractional item rejected: %v", err)
	}
}

func TestDecodeLineItemsDistinguishesMalformedFromEmpty(t *testing.T) {
	for _, input := range []string{"", "[]"} {
		items, err := DecodeLineItems(input)
		if err != nil || len(items) != 0 {
			t.Errorf("DecodeLineItems(%q) = (%v, %v), want empty, nil", input, items, err)
		}
	}

	_, err := DecodeLineItems(`[`)
	if !errors.Is(err, ErrMalformedLineItems) {
		t.Fatalf("malformed error = %v, want ErrMalformedLineItems", err)
	}
}

func TestLineAmountUsesDecimalSafeFractionalArithmetic(t *testing.T) {
	amount, err := LineAmount(LineItem{
		Title: "Materials", UnitPrice: 10.25, Quantity: 1.5, Discount: 0.40, Surcharge: 0.15,
	})
	if err != nil {
		t.Fatalf("LineAmount() error = %v", err)
	}
	if got := amount.MinorUnits(); got != 1513 {
		t.Errorf("LineAmount() = %d minor units, want 1513", got)
	}
}

func TestCalculateTotalsRoundsTaxOnceOnAggregateTaxableSubtotal(t *testing.T) {
	items := []LineItem{
		{Title: "Taxable A", UnitPrice: 0.05, Quantity: 1, Taxable: true, TaxRate: "99"},
		{Title: "Taxable B", UnitPrice: 0.05, Quantity: 1, Taxable: true, TaxRate: "0"},
		{Title: "Exempt", UnitPrice: 1, Quantity: 1, Taxable: false, TaxRate: "100"},
	}
	totals, err := CalculateTotals(items, "5")
	if err != nil {
		t.Fatalf("CalculateTotals() error = %v", err)
	}
	if got := totals.Subtotal.MinorUnits(); got != 110 {
		t.Errorf("subtotal = %d, want 110", got)
	}
	if got := totals.TaxableSubtotal.MinorUnits(); got != 10 {
		t.Errorf("taxable subtotal = %d, want 10", got)
	}
	if got := totals.Tax.MinorUnits(); got != 1 {
		t.Errorf("aggregate tax = %d, want 1 (half away from zero)", got)
	}
	if got := totals.Total.MinorUnits(); got != 111 {
		t.Errorf("total = %d, want 111", got)
	}
}

func TestCalculateTotalsValidatesPercentageRate(t *testing.T) {
	item := []LineItem{{Title: "x", Quantity: 1}}
	for _, rate := range []string{"", "nope", "-1", "100.01"} {
		if _, err := CalculateTotals(item, rate); !errors.Is(err, ErrInvalidTaxRate) {
			t.Errorf("rate %q error = %v, want ErrInvalidTaxRate", rate, err)
		}
	}
	for _, rate := range []string{"0", "8.25", "100", "8.25%"} {
		if _, err := CalculateTotals(item, rate); err != nil {
			t.Errorf("rate %q rejected: %v", rate, err)
		}
	}
}

func TestEncodeLineItemsValidatesDomainData(t *testing.T) {
	if _, err := EncodeLineItems([]LineItem{{Title: "x", Quantity: math.NaN()}}); !errors.Is(err, ErrInvalidLineItem) {
		t.Fatalf("EncodeLineItems() error = %v, want ErrInvalidLineItem", err)
	}
	encoded, err := EncodeLineItems(nil)
	if err != nil || encoded != "[]" {
		t.Fatalf("EncodeLineItems(nil) = (%q, %v), want ([], nil)", encoded, err)
	}
}

func TestEncodeLineItemsFractionalQuantityRoundTrip(t *testing.T) {
	want := []LineItem{{Title: "Labor", UnitPrice: 12.5, Quantity: 1.25}}
	encoded, err := EncodeLineItems(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeLineItems(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Quantity != want[0].Quantity {
		t.Fatalf("round trip = %#v, want quantity %v", got, want[0].Quantity)
	}
}

func TestLineAndTaxRoundingHalfAwayFromZero(t *testing.T) {
	positive, err := LineAmount(LineItem{Title: "x", UnitPrice: 0.005, Quantity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if positive.MinorUnits() != 1 {
		t.Errorf("positive half-cent = %d, want 1", positive.MinorUnits())
	}

	negative, err := LineAmount(LineItem{Title: "x", UnitPrice: 0, Quantity: 1, Discount: 0.005})
	if err != nil {
		t.Fatal(err)
	}
	if negative.MinorUnits() != -1 {
		t.Errorf("negative half-cent = %d, want -1", negative.MinorUnits())
	}
}

func TestCatalogSnapshotCreatesFrozenLineItem(t *testing.T) {
	item := &ent.Item{
		ID:          42,
		Name:        "Service call",
		Description: "Original description",
		UnitPrice:   125.50,
		Taxable:     true,
		TaxRate:     "8.25",
	}

	snapshot := CatalogSnapshotFromItem(item)
	line, err := snapshot.NewLineItem(2.5)
	if err != nil {
		t.Fatal(err)
	}
	item.Name = "Changed catalog name"
	item.Description = "Changed catalog description"
	item.UnitPrice = 1
	item.Taxable = false

	want := LineItem{
		ItemID:      42,
		Title:       "Service call",
		Description: "Original description",
		UnitPrice:   125.50,
		Quantity:    2.5,
		Taxable:     true,
		TaxRate:     "8.25",
	}
	if line != want {
		t.Fatalf("NewLineItem() = %#v, want frozen snapshot %#v", line, want)
	}
}

func TestCatalogSnapshotQuantityValidation(t *testing.T) {
	snapshot := CatalogSnapshot{ID: 1, Name: "Labor", UnitPrice: 10}
	for _, quantity := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := snapshot.NewLineItem(quantity); !errors.Is(err, ErrInvalidLineItem) {
			t.Errorf("NewLineItem(%v) error = %v, want ErrInvalidLineItem", quantity, err)
		}
	}
}

func TestReferencesItemUsesProvenanceIDOnly(t *testing.T) {
	items := []LineItem{
		{ItemID: 0, Title: "Duplicate", Quantity: 1},
		{ItemID: 7, Title: "Duplicate", Quantity: 1},
	}
	if !ReferencesItem(items, 7) {
		t.Error("ReferencesItem(items, 7) = false, want true")
	}
	for _, itemID := range []int64{0, -1, 8} {
		if ReferencesItem(items, itemID) {
			t.Errorf("ReferencesItem(items, %d) = true, want false", itemID)
		}
	}
}

type testCustomerTaxContext struct {
	defaultRate string
	exemptions  map[int64]bool
}

func (c testCustomerTaxContext) DefaultTaxRate() string                { return c.defaultRate }
func (c testCustomerTaxContext) CustomerTaxExemptions() map[int64]bool { return c.exemptions }

func TestEncodeEditorBootstrapIsDeterministicAndPreservesSnapshots(t *testing.T) {
	catalog := []CatalogSnapshot{
		{ID: 2, Name: "Duplicate", Description: "new", UnitPrice: 99, Taxable: true, TaxRate: "9"},
		{ID: 1, Name: "Duplicate", UnitPrice: 10},
	}
	existing := `[{"item_id":2,"title":"Edited title","description":"frozen","unit_price":12.5,"quantity":2,"taxable":false,"tax_rate":"5","discount":1,"surcharge":3},{"item_id":0,"title":"Free text","description":"manual","unit_price":4,"quantity":1,"taxable":false,"tax_rate":"","discount":0,"surcharge":0}]`
	taxContext := testCustomerTaxContext{defaultRate: "8.25", exemptions: map[int64]bool{9: true, 3: false}}

	first, err := EncodeEditorBootstrap(catalog, existing, taxContext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EncodeEditorBootstrap(catalog, existing, taxContext)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("bootstrap output is not deterministic:\nfirst:  %#v\nsecond: %#v", first, second)
	}
	if first.ItemsJSON != `[{"id":2,"name":"Duplicate","description":"new","unit_price":99,"taxable":true,"tax_rate":"9"},{"id":1,"name":"Duplicate","description":"","unit_price":10,"taxable":false,"tax_rate":""}]` {
		t.Errorf("ItemsJSON = %s", first.ItemsJSON)
	}
	if first.ExistingItemsJSON != existing {
		t.Errorf("ExistingItemsJSON = %s, want frozen lines %s", first.ExistingItemsJSON, existing)
	}
	if first.CustomersJSON != `{"default_tax_rate":"8.25","customers":{"3":{"tax_exempt":false},"9":{"tax_exempt":true}}}` {
		t.Errorf("CustomersJSON = %s", first.CustomersJSON)
	}
}

func TestEncodeEditorBootstrapRejectsMalformedOrInvalidExistingLines(t *testing.T) {
	context := testCustomerTaxContext{defaultRate: "0"}
	if _, err := EncodeEditorBootstrap(nil, `[`, context); !errors.Is(err, ErrMalformedLineItems) {
		t.Fatalf("malformed existing lines error = %v, want ErrMalformedLineItems", err)
	}
	if _, err := EncodeEditorBootstrap(nil, `[{"title":"","quantity":0}]`, context); !errors.Is(err, ErrInvalidLineItem) {
		t.Fatalf("invalid existing line error = %v, want ErrInvalidLineItem", err)
	}
}
