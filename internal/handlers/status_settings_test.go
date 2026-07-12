package handlers

import (
	"net/url"
	"testing"
)

func TestStatusMoveOrderPrefersValidRequestedOrder(t *testing.T) {
	tests := []struct {
		name string
		form url.Values
		want int
	}{
		{name: "clicked reorder wins", form: url.Values{"order": {"6"}, "requested_order": {"2"}}, want: 2},
		{name: "numeric field fallback", form: url.Values{"order": {"6"}}, want: 6},
		{name: "invalid requested order falls back", form: url.Values{"order": {"6"}, "requested_order": {"0"}}, want: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statusMoveOrder(tt.form); got != tt.want {
				t.Fatalf("statusMoveOrder() = %d, want %d", got, tt.want)
			}
		})
	}
}
