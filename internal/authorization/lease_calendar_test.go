package authorization

import (
	"testing"
	"time"
)

func TestLeaseCalendarBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, state, start, end, today, lifecycle, from, through string }{
		{"incomplete Draft", "draft", "", "", "2024-02-29", "draft", "", ""},
		{"future", "active", "2030-01-01", "2030-01-30", "2029-12-31", "scheduled", "", ""},
		{"commencement inclusive", "scheduled", "2024-01-01", "2024-01-30", "2024-01-01", "active", "", ""},
		{"fixed end inclusive", "active", "2024-01-01", "2024-01-30", "2024-01-30", "active", "", ""},
		{"next day", "active", "2024-01-01", "2024-01-30", "2024-01-31", "month_to_month", "2024-01-31", "2024-02-28"},
		{"leap clamp", "active", "2024-01-01", "2024-01-30", "2024-02-29", "month_to_month", "2024-02-29", "2024-03-30"},
		{"no permanent drift", "active", "2024-01-01", "2024-01-30", "2024-03-31", "month_to_month", "2024-03-31", "2024-04-29"},
		{"historical century nonleap", "active", "1899-01-01", "1900-01-30", "1900-02-28", "month_to_month", "1900-02-28", "1900-03-30"},
		{"future century leap", "active", "2399-01-01", "2400-01-30", "2400-02-29", "month_to_month", "2400-02-29", "2400-03-30"},
		{"before clamped boundary", "active", "2023-01-01", "2023-01-29", "2023-03-29", "month_to_month", "2023-02-28", "2023-03-29"},
		{"terminated", "terminated", "2024-01-01", "2024-01-30", "2025-01-01", "terminated", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			today, err := time.Parse(time.DateOnly, tc.today)
			if err != nil {
				t.Fatal(err)
			}
			got, err := leaseCalendar(tc.state, tc.start, tc.end, today)
			if err != nil {
				t.Fatal(err)
			}
			if got.Lifecycle != tc.lifecycle || got.RenewalStartsOn != tc.from || got.RenewalEndsOn != tc.through {
				t.Fatalf("calendar = %+v", got)
			}
		})
	}
}

func TestLeaseCalendarCompanyLocalDST(t *testing.T) {
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ instant, want string }{
		{"2024-03-10T04:59:59Z", "active"},
		{"2024-03-10T05:00:00Z", "month_to_month"},
		{"2024-03-10T07:00:00Z", "month_to_month"},
	} {
		t.Run(tc.instant, func(t *testing.T) {
			instant, err := time.Parse(time.RFC3339, tc.instant)
			if err != nil {
				t.Fatal(err)
			}
			got, err := leaseCalendar("active", "2024-01-01", "2024-03-09", instant.In(zone))
			if err != nil || got.Lifecycle != tc.want {
				t.Fatalf("calendar=%+v error=%v", got, err)
			}
		})
	}
}
