package services

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAPIMutationActiveEntryConflictMapping(t *testing.T) {
	matching := fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505", ConstraintName: activeTimeEntryIndex})
	if !isAPIMutationActiveEntryConflict(matching) {
		t.Fatal("active-entry partial unique violation was not recognized")
	}
	if isAPIMutationActiveEntryConflict(&pgconn.PgError{Code: "23505", ConstraintName: "other_unique_index"}) {
		t.Fatal("unrelated unique violation was recognized")
	}
}
