// Package testutil provides assertions for integration fixtures, not runtime code.
package testutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SequenceState struct {
	LastValue int64
	IsCalled  bool
}

// Sequences captures allocation state, including is_called (which pg_sequences
// alone cannot distinguish from an inaccessible last_value).
func Sequences(t testing.TB, ctx context.Context, pool *pgxpool.Pool) map[string]SequenceState {
	t.Helper()
	var names []string
	if err := pool.QueryRow(ctx, `SELECT array_agg(sequencename::text ORDER BY sequencename) FROM pg_sequences WHERE schemaname=current_schema()`).Scan(&names); err != nil {
		t.Fatal(err)
	}
	result := map[string]SequenceState{}
	for _, name := range names {
		var value SequenceState
		if err := pool.QueryRow(ctx, `SELECT last_value,is_called FROM `+pgx.Identifier{name}.Sanitize()).Scan(&value.LastValue, &value.IsCalled); err != nil {
			t.Fatal(err)
		}
		result[name] = value
	}
	return result
}

// AssertSequenceUpgrade requires exact preservation of every existing sequence
// and permits only the named new sequences, each still at its unallocated start.
func AssertSequenceUpgrade(t testing.TB, ctx context.Context, pool *pgxpool.Pool, before map[string]SequenceState, added ...string) {
	t.Helper()
	after := Sequences(t, ctx, pool)
	for name, want := range before {
		got, present := after[name]
		if !present || got != want {
			t.Fatalf("upgrade changed existing sequence %s: present=%t got=%+v want=%+v", name, present, got, want)
		}
		delete(after, name)
	}
	for _, name := range added {
		got, present := after[name]
		if !present || got != (SequenceState{LastValue: 1, IsCalled: false}) {
			t.Fatalf("new sequence %s is missing or allocated: present=%t state=%+v", name, present, got)
		}
		delete(after, name)
	}
	if len(after) != 0 {
		t.Fatalf("unexpected upgrade sequences: %+v", after)
	}
}

// AssertNoInvoiceLeaseBackfill is paired with comparisons excluding precisely
// the newly added lease_id key. A non-NULL invented relationship must still fail.
func AssertNoInvoiceLeaseBackfill(t testing.TB, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var invented bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoices WHERE lease_id IS NOT NULL)`).Scan(&invented); err != nil {
		t.Fatal(err)
	}
	if invented {
		t.Fatal("upgrade invented an Invoice Lease relationship")
	}
}
