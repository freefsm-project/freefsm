package authorization

import "testing"

func TestLeaseOrdinaryNoticeBoundary(t *testing.T) {
	for _, tc := range []struct{ end, notice, want string }{
		{"2024-01-30", "2023-12-31", "2024-01-30"},
		{"2024-01-30", "2024-01-01", "2024-02-28"},
		{"2024-01-30", "2024-01-31", "2024-03-30"},
		{"2023-01-30", "2023-01-01", "2023-02-27"},
		{"2024-06-30", "2024-06-01", "2024-07-31"},
		{"2024-06-30", "2024-05-31", "2024-06-30"},
	} {
		t.Run(tc.end+"/"+tc.notice, func(t *testing.T) {
			got, err := leaseOrdinaryNoticeEnd(tc.end, tc.notice)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %s", got, err, tc.want)
			}
		})
	}
}
