package services

import "testing"

func TestParsePaymentsBackfillsLegacyIDs(t *testing.T) {
	t.Parallel()

	payments, err := ParsePayments(`[{"amount":25,"method":"cash","reference":"A","date":"2026-07-03","notes":""}]`)
	if err != nil {
		t.Fatalf("ParsePayments: %v", err)
	}
	if len(payments) != 1 {
		t.Fatalf("len(payments) = %d, want 1", len(payments))
	}
	if payments[0].ID == "" {
		t.Fatal("legacy payment ID was not backfilled")
	}

	again, err := ParsePayments(`[{"amount":25,"method":"cash","reference":"A","date":"2026-07-03","notes":""}]`)
	if err != nil {
		t.Fatalf("ParsePayments again: %v", err)
	}
	if again[0].ID != payments[0].ID {
		t.Fatalf("legacy payment ID = %q, want stable %q", again[0].ID, payments[0].ID)
	}
}

func TestSerializePaymentsPreservesIDs(t *testing.T) {
	t.Parallel()

	payments, err := ParsePayments(SerializePayments([]Payment{{ID: "payment-1", Amount: 10, Method: "check"}}))
	if err != nil {
		t.Fatalf("ParsePayments: %v", err)
	}
	if got, want := payments[0].ID, "payment-1"; got != want {
		t.Fatalf("payment ID = %q, want %q", got, want)
	}
}
