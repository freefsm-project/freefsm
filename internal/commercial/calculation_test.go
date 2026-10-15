package commercial_test

import (
	"testing"

	"github.com/freefsm-project/freefsm/internal/commercial"
)

// Worked COM-03 vector: 0.335 × $3.00 = $1.01; 50% discount leaves
// $0.51; 10% surcharge produces $0.56. Tax is rounded for each line.
func TestStagedRoundingAndPerLineTax(t *testing.T) {
	in := commercial.Input{TaxRate: "10", Lines: []commercial.Line{
		{Quantity: "0.335", UnitPrice: "3.00", Taxable: true, Discount: &commercial.Adjustment{Kind: "percentage", Value: "50"}, Surcharge: &commercial.Adjustment{Kind: "percentage", Value: "10"}},
		{Quantity: "1", UnitPrice: "0.05", Taxable: true},
	}}
	got, err := commercial.Calculate(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Lines[0].BaseCents != 101 || got.Lines[0].AdjustedCents != 56 || got.PretaxCents != 61 || got.TaxCents != 7 || got.TotalCents != 68 {
		t.Fatalf("unexpected accepted amounts: %+v", got)
	}
}

func TestDocumentAllocationReconcilesWithStableTies(t *testing.T) {
	// Three equal cents: a one-cent discount goes to the first line. A
	// one-cent surcharge then goes to the first remaining positive line.
	got, err := commercial.Calculate(commercial.Input{TaxRate: "50", Discount: &commercial.Adjustment{Kind: "fixed", Value: "0.01"}, Surcharge: &commercial.Adjustment{Kind: "fixed", Value: "0.01"}, Lines: []commercial.Line{
		{Quantity: "1", UnitPrice: "0.01", Taxable: true},
		{Quantity: "1", UnitPrice: "0.01", Taxable: true},
		{Quantity: "1", UnitPrice: "0.01", Taxable: false},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Lines[0].AdjustedCents != 0 || got.Lines[1].AdjustedCents != 2 || got.Lines[2].AdjustedCents != 1 || got.PretaxCents != 3 || got.TaxCents != 1 || got.TotalCents != 4 {
		t.Fatalf("document allocation or per-line tax did not reconcile: %+v", got)
	}
}

func TestUnitPriceMustFitExactCentsEvenWithSmallQuantity(t *testing.T) {
	_, err := commercial.Calculate(commercial.Input{TaxRate: "0", Lines: []commercial.Line{{Quantity: "0.0000001", UnitPrice: "92233720368547758.08"}}})
	if err == nil {
		t.Fatal("accepted a unit price larger than int64 cents")
	}
}

func TestCommercialGoldenBoundaries(t *testing.T) {
	fixed := func(v string) *commercial.Adjustment { return &commercial.Adjustment{Kind: "fixed", Value: v} }
	percent := func(v string) *commercial.Adjustment { return &commercial.Adjustment{Kind: "percentage", Value: v} }
	for _, tc := range []struct {
		name               string
		input              commercial.Input
		pretax, tax, total int64
		invalid            bool
	}{
		{"empty", commercial.Input{TaxRate: "0"}, 0, 0, 0, false},
		{"half negative", commercial.Input{TaxRate: "0", Lines: []commercial.Line{{Quantity: "1", UnitPrice: "1.00"}, {Quantity: "0.5", UnitPrice: "-0.01"}}}, 99, 0, 99, false},
		{"exempt", commercial.Input{TaxRate: "100", TaxExempt: true, Lines: []commercial.Line{{Quantity: "1", UnitPrice: "1.00", Taxable: true}}}, 100, 0, 100, false},
		{"discount exhaustion", commercial.Input{TaxRate: "10", Discount: fixed("1000.00"), Surcharge: percent("10"), Lines: []commercial.Line{{Quantity: "1", UnitPrice: "1.00", Taxable: true}}}, 0, 0, 0, false},
		{"unallocatable surcharge", commercial.Input{TaxRate: "0", Surcharge: fixed("0.01")}, 0, 0, 0, true},
		{"zero surcharge", commercial.Input{TaxRate: "0", Surcharge: fixed("0.00")}, 0, 0, 0, false},
		{"negative pretax", commercial.Input{TaxRate: "0", Lines: []commercial.Line{{Quantity: "1", UnitPrice: "-0.01"}}}, 0, 0, 0, true},
		{"negative final", commercial.Input{TaxRate: "100", Lines: []commercial.Line{{Quantity: "1", UnitPrice: "0.02"}, {Quantity: "1", UnitPrice: "-0.02", Taxable: true}}}, 0, 0, 0, true},
		{"negative adjustment even rounded zero", commercial.Input{TaxRate: "0", Lines: []commercial.Line{{Quantity: "0.01", UnitPrice: "-0.01", Discount: fixed("0.00")}}}, 0, 0, 0, true},
		{"nonfinite quantity", commercial.Input{TaxRate: "0", Lines: []commercial.Line{{Quantity: "NaN", UnitPrice: "1.00"}}}, 0, 0, 0, true},
		{"zero quantity", commercial.Input{TaxRate: "0", Lines: []commercial.Line{{Quantity: "0", UnitPrice: "1.00"}}}, 0, 0, 0, true},
		{"negative quantity", commercial.Input{TaxRate: "0", Lines: []commercial.Line{{Quantity: "-1", UnitPrice: "1.00"}}}, 0, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := commercial.Calculate(tc.input)
			if tc.invalid {
				if err == nil {
					t.Fatalf("accepted invalid input: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.PretaxCents != tc.pretax || got.TaxCents != tc.tax || got.TotalCents != tc.total {
				t.Fatalf("got %+v; want %d/%d/%d", got, tc.pretax, tc.tax, tc.total)
			}
		})
	}
}
