package authorization

import (
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
