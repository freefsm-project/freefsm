package handlers

import "testing"

func TestDecodeAndValidateLineItems(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		taxRate string
		wantErr bool
		wantLen int
	}{
		{name: "malformed", raw: `[`, taxRate: "0", wantErr: true},
		{name: "zero quantity", raw: `[{"title":"Work","quantity":0}]`, taxRate: "0", wantErr: true},
		{name: "negative quantity", raw: `[{"title":"Work","quantity":-1}]`, taxRate: "0", wantErr: true},
		{name: "fractional quantity", raw: `[{"title":"Work","quantity":0.25}]`, taxRate: "8.25", wantLen: 1},
		{name: "invalid tax rate", raw: `[{"title":"Work","quantity":1}]`, taxRate: "101", wantErr: true},
		{name: "explicit empty list", raw: `[]`, taxRate: "8.25", wantLen: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := decodeAndValidateLineItems(tt.raw, tt.taxRate)
			if (err != nil) != tt.wantErr {
				t.Fatalf("decodeAndValidateLineItems() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (items == nil || len(items) != tt.wantLen) {
				t.Fatalf("decodeAndValidateLineItems() items = %#v, want non-nil length %d", items, tt.wantLen)
			}
		})
	}
}
