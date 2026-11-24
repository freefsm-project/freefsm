package services

import (
	"math"
	"testing"
	"time"
)

func TestFinancialReportCalendarBoundaries(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, date := range []string{"2026-03-08", "2026-11-01"} {
		start, end, err := financialDateBounds(FinancialReportFilter{From: date, To: date}, loc)
		if err != nil {
			t.Fatal(err)
		}
		want := 23 * time.Hour
		if date == "2026-11-01" {
			want = 25 * time.Hour
		}
		if end.Sub(*start) != want {
			t.Fatalf("%s day=%v", date, end.Sub(*start))
		}
		// One second before next midnight is still the same company calendar day,
		// even though its UTC date is the following day.
		asOf := end.Add(-time.Second)
		today, err := time.Parse(time.DateOnly, date)
		if err != nil {
			t.Fatal(err)
		}
		for _, test := range []struct{ days, bucket int }{{-1, 0}, {0, 0}, {1, 1}, {30, 1}, {31, 2}, {60, 2}, {61, 3}, {90, 3}, {91, 4}} {
			due := today.AddDate(0, 0, -test.days).Format(time.DateOnly)
			got, err := financialAgingBucket(due, asOf, loc)
			if err != nil || got != test.bucket {
				t.Fatalf("%s due %s bucket=%d err=%v", date, due, got, err)
			}
		}
		for _, due := range []string{"", "0001-01-01"} {
			got, err := financialAgingBucket(due, asOf, loc)
			if err != nil || got != 5 {
				t.Fatalf("missing due=%q bucket=%d err=%v", due, got, err)
			}
		}
	}
}

func TestFinancialReportCentsOverflowFails(t *testing.T) {
	for _, test := range [][2]int64{{math.MaxInt64, 1}, {math.MinInt64, -1}} {
		n := test[0]
		if err := financialAdd(&n, test[1]); err == nil || n != test[0] {
			t.Fatalf("overflow mutated or accepted: %d", n)
		}
	}
}
