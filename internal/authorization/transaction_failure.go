package authorization

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TransactionFailure distinguishes a rejected transaction from a transport
// failure during COMMIT, where the caller must reconcile the original identity.
type TransactionFailure struct {
	Err        error
	RolledBack bool
}

func (e *TransactionFailure) Error() string { return e.Err.Error() }
func (e *TransactionFailure) Unwrap() error { return e.Err }

func commitRejected(err error) bool {
	var pg *pgconn.PgError
	if errors.Is(err, pgx.ErrTxCommitRollback) {
		return true
	}
	if !errors.As(err, &pg) || pg.Code == "40003" {
		return false
	}
	// Constraint violations, explicit transaction rollback and a deferred
	// trigger's RAISE EXCEPTION confirm rejection. Connection/system errors do
	// not establish the commit outcome, even when represented as a PgError.
	return strings.HasPrefix(pg.Code, "23") || strings.HasPrefix(pg.Code, "40") || pg.Code == "P0001"
}
