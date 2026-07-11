package settlement

import (
	"testing"
	"time"
)

func TestSettlementState(t *testing.T) {
	for _, tt := range []struct {
		total, settled int64
		want           State
	}{
		{0, 0, Paid}, {100, 0, Unpaid}, {100, 40, PartiallyPaid}, {100, 100, Paid}, {100, 120, Paid},
	} {
		got, err := StateFor(tt.total, tt.settled)
		if err != nil || got != tt.want {
			t.Fatalf("StateFor(%d, %d) = %q, %v; want %q", tt.total, tt.settled, got, err, tt.want)
		}
	}
	if _, err := StateFor(-1, 0); err == nil {
		t.Fatal("negative invoice total accepted")
	}
}

func TestSplitPayment(t *testing.T) {
	applied, credit, err := SplitPayment(125, 80)
	if err != nil || applied != 80 || credit != 45 {
		t.Fatalf("SplitPayment = %d, %d, %v", applied, credit, err)
	}
	if _, _, err := SplitPayment(0, 10); err == nil {
		t.Fatal("zero payment accepted")
	}
}

func TestFIFO(t *testing.T) {
	d1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d2 := d1.AddDate(0, 0, 1)
	got, err := AllocateFIFO(70, []AvailableSource{{ID: "b", Date: d1, Cents: 30}, {ID: "a", Date: d1, Cents: 20}, {ID: "c", Date: d2, Cents: 40}})
	if err != nil {
		t.Fatal(err)
	}
	want := []Allocation{{SourceID: "a", Cents: 20}, {SourceID: "b", Cents: 30}, {SourceID: "c", Cents: 20}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allocation %d = %#v, want %#v", i, got[i], want[i])
		}
	}
	if _, err := AllocateFIFO(91, []AvailableSource{{ID: "a", Date: d1, Cents: 90}}); err == nil {
		t.Fatal("over-refund accepted")
	}
}

func TestEffectiveDate(t *testing.T) {
	today := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	if err := ValidateEffectiveDate(today.AddDate(0, 0, 1), today, time.Time{}); err == nil {
		t.Fatal("future date accepted")
	}
	if err := ValidateEffectiveDate(today.AddDate(0, 0, -2), today, today.AddDate(0, 0, -1)); err == nil {
		t.Fatal("date before source accepted")
	}
}
