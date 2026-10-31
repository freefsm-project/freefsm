package authorization

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestLeaseNoticeCommitOutcomeClassification(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		rejected bool
	}{
		{"aborted transaction", pgx.ErrTxCommitRollback, true},
		{"deferred invariant", &pgconn.PgError{Code: "23514"}, true},
		{"serialization rejection", &pgconn.PgError{Code: "40001"}, true},
		{"statement completion unknown", &pgconn.PgError{Code: "40003"}, false},
		{"transaction resolution unknown", &pgconn.PgError{Code: "08007"}, false},
		{"server shutdown", &pgconn.PgError{Code: "57P01"}, false},
		{"connection lost", errors.New("connection lost during commit"), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := commitRejected(test.err); got != test.rejected {
				t.Fatalf("known rejection = %v, want %v", got, test.rejected)
			}
		})
	}
}
