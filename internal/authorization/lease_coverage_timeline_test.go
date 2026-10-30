package authorization

import (
	"testing"
)

func TestLeaseCoverageTimeline(t *testing.T) {
	initial := []LeaseAssetDependency{{AssetID: 1, StartsOn: "2026-01-01"}}
	t.Run("atomic replacement and inclusive boundaries", func(t *testing.T) {
		rows, err := projectLeaseCoverage(initial, "2026-01-01", nil, []LeaseCoverageEvent{{Kind: "replace", EffectiveOn: "2026-02-10", OutgoingID: 1, IncomingID: 2}})
		if err != nil || len(rows) != 2 || rows[0].EndsOn == nil || *rows[0].EndsOn != "2026-02-09" || rows[1].StartsOn != "2026-02-10" {
			t.Fatalf("replacement: %+v %v", rows, err)
		}
	})
	t.Run("future gap cannot be repaired by later add", func(t *testing.T) {
		_, err := projectLeaseCoverage(initial, "2026-01-01", nil, []LeaseCoverageEvent{{Kind: "remove", EffectiveOn: "2026-02-10", OutgoingID: 1}, {Kind: "add", EffectiveOn: "2026-03-01", IncomingID: 2}})
		if err == nil {
			t.Fatal("accepted an empty in-force interval")
		}
	})
	t.Run("future event references must survive correction", func(t *testing.T) {
		_, err := projectLeaseCoverage(initial, "2026-01-01", nil, []LeaseCoverageEvent{{Kind: "replace", EffectiveOn: "2026-02-10", OutgoingID: 1, IncomingID: 2}, {Kind: "remove", EffectiveOn: "2026-03-01", OutgoingID: 1}})
		if err == nil {
			t.Fatal("accepted removal of no longer covered Asset")
		}
	})
	t.Run("historical re-add retains distinct intervals", func(t *testing.T) {
		rows, err := projectLeaseCoverage(initial, "2026-01-01", nil, []LeaseCoverageEvent{{Kind: "replace", EffectiveOn: "2026-02-10", OutgoingID: 1, IncomingID: 2}, {Kind: "replace", EffectiveOn: "2026-03-01", OutgoingID: 2, IncomingID: 1}})
		if err != nil || len(rows) != 3 {
			t.Fatalf("re-add: %+v %v", rows, err)
		}
	})
	for _, day := range []string{"2025-12-31", "2026-04-01"} {
		t.Run(day, func(t *testing.T) {
			end := "2026-03-31"
			_, err := projectLeaseCoverage(initial, "2026-01-01", &end, []LeaseCoverageEvent{{Kind: "add", EffectiveOn: day, IncomingID: 2}})
			if err == nil {
				t.Fatal("accepted coverage outside agreement")
			}
		})
	}
}

func TestLeaseCoverageBillingMonthBoundary(t *testing.T) {
	zero, rent := int64(0), int64(4200)
	terms := []DraftLeaseTerms{{AssetID: 1, Selected: true, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}}
	records := []LeaseCoverageRecord{{LeaseCoverageInput: LeaseCoverageInput{LeaseCoverageEvent: LeaseCoverageEvent{ID: 1, Kind: "replace", EffectiveOn: "2026-02-01", OutgoingID: 1, IncomingID: 2}, Action: "create"}}}
	for _, tc := range []struct {
		due   string
		asset int64
	}{{"2026-02-01", 1}, {"2026-03-01", 2}} {
		got, err := coverageBillingTerms(terms, records, tc.due, false)
		if err != nil || len(got) != 1 || got[0].AssetID != tc.asset || *got[0].RentCents != rent {
			t.Fatalf("%s: %+v %v", tc.due, got, err)
		}
	}
}
