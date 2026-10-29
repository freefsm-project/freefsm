package authorization

import (
	"math"
	"testing"

	"github.com/freefsm-project/freefsm/internal/commercial"
)

func TestLeaseObligationTaxRepresentability(t *testing.T) {
	amount := int64(101)
	for _, tc := range []struct {
		name, first, second string
		exempt, invalid     bool
		tax                 int64
	}{
		{"equal numeric rates", "5", "5.000", false, false, 10},
		{"zero line stays nontaxable", "5", "0", false, false, 5},
		{"distinct exact rates", "5", "5.00000000000000000001", false, true, 0},
		{"exemption removes conflict", "5", "7", true, false, 0},
		{"exemption does not excuse malformed terms", "5", "", true, true, 0},
		{"all zero", "0", "0.000", false, false, 0},
		{"maximum rate", "100", "100.00", false, false, 202},
		{"exemption does not excuse excessive rate", "5", "100.00001", true, true, 0},
		{"negative rate", "5", "-1", true, true, 0},
		{"rational notation rejected", "5", "1/2", true, true, 0},
		{"nonfinite rate", "5", "NaN", true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terms := []DraftLeaseTerms{{AssetID: 1, InitialCents: &amount, RentCents: &amount, TaxRate: tc.first, Selected: true}, {AssetID: 2, InitialCents: &amount, RentCents: &amount, TaxRate: tc.second, Selected: true}}
			in, err := leaseObligationInput(terms, false, tc.exempt)
			if tc.invalid {
				if err == nil {
					t.Fatal("unrepresentable terms accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := commercial.Calculate(in)
			if err != nil {
				t.Fatal(err)
			}
			if result.TaxCents != tc.tax || result.PretaxCents != 202 {
				t.Fatalf("unexpected result: %+v", result)
			}
		})
	}
}

func TestLeaseObligationInvalidAmounts(t *testing.T) {
	for _, tc := range []struct {
		name          string
		initial, rent int64
	}{
		{"negative initial", -1, 100},
		{"negative rent", 100, -1},
		{"initial total overflow", math.MaxInt64, 100},
		{"monthly total overflow", 100, math.MaxInt64},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terms := []DraftLeaseTerms{{AssetID: 1, InitialCents: &tc.initial, RentCents: &tc.rent, TaxRate: "0", Selected: true}, {AssetID: 2, InitialCents: &tc.initial, RentCents: &tc.rent, TaxRate: "0", Selected: true}}
			_, initialErr := leaseObligationInput(terms, false, true)
			_, monthlyErr := leaseObligationInput(terms, true, true)
			if initialErr == nil && monthlyErr == nil {
				t.Fatal("invalid amounts accepted even under exemption")
			}
		})
	}
}
