package saveaction

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrExecutionFenced means this dispatch no longer owns an executable attempt.
// It says nothing about a different dispatch or a historical committed receipt.
var ErrExecutionFenced = errors.New("online action execution is fenced; reconcile the same identity")

// AdmitExecution admits a single dispatch, identified before its INSERT. A lost
// response is unknown: callers must query the identity, not dispatch on error.
// Duplicate requests never acquire execution authority from a receipt lookup.
func (s Store) AdmitExecution(ctx context.Context, a Action, digest, method, path, review string) (bool, error) {
	if _, err := uuid.Parse(a.ID); err != nil {
		return false, ErrIdentity
	}
	if _, err := uuid.Parse(a.ExecutionToken); err != nil {
		return false, ErrIdentity
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO online_save_actions(user_id,operation_id,intent_hash,method,request_path,review_path,execution_token) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, a.UserID, a.ID, digest, method, path, review, a.ExecutionToken)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var matches bool
	err = s.Pool.QueryRow(ctx, `SELECT intent_hash=$3 AND method=$4 AND request_path=$5 AND execution_token IS NOT NULL FROM online_save_actions WHERE user_id=$1 AND operation_id=$2`, a.UserID, a.ID, digest, method, path).Scan(&matches)
	if err != nil {
		return false, err
	}
	if !matches {
		return false, ErrIdentity
	}
	return false, nil
}

// LockExecution runs after the authority mutex and before any domain row lock
// or effect. Reconciliation locks this same row, so a paused admitted dispatch
// cannot execute after reconciliation has declared its nonexecution.
func LockExecution(ctx context.Context, tx pgx.Tx) error {
	a, ok := ctx.Value(key{}).(Action)
	if !ok || a.ExecutionToken == "" {
		return nil
	}
	if !a.Atomic {
		return ErrIdentity
	}
	var executable bool
	err := tx.QueryRow(ctx, `SELECT execution_token=$3 AND NOT execution_fenced AND domain_committed_at IS NULL AND completed_at IS NULL AND request_path=$4 FROM online_save_actions WHERE user_id=$1 AND operation_id=$2 FOR UPDATE`, a.UserID, a.ID, a.ExecutionToken, a.Path).Scan(&executable)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !executable {
		return ErrExecutionFenced
	}
	return err
}

// FenceExecution is called by the owning command's terminal reconciliation
// transaction AFTER acquiring authority. Missing history stays unknown. A
// committed result stays committed. The caller must inspect current domain
// state under its locks before committing this nonexecution decision.
// No retry is granted here; RetryExecution requires an explicit reviewed retry.
func FenceExecution(ctx context.Context, tx pgx.Tx, userID int64, id, path string) (string, error) {
	var token *string
	var committed bool
	err := tx.QueryRow(ctx, `SELECT execution_token,domain_committed_at IS NOT NULL FROM online_save_actions WHERE user_id=$1 AND operation_id=$2 AND request_path=$3 FOR UPDATE`, userID, id, path).Scan(&token, &committed)
	if errors.Is(err, pgx.ErrNoRows) {
		return "unknown", nil
	}
	if err != nil {
		return "unknown", err
	}
	if token == nil {
		return "unknown", ErrIdentity
	}
	if committed {
		return "saved", nil
	}
	tag, err := tx.Exec(ctx, `UPDATE online_save_actions a SET execution_fenced=true,disposition='rejected' WHERE user_id=$1 AND operation_id=$2 AND completed_at IS NULL AND domain_committed_at IS NULL AND NOT EXISTS(SELECT 1 FROM online_save_effects e WHERE e.user_id=a.user_id AND e.operation_id=a.operation_id)`, userID, id)
	if err != nil {
		return "unknown", err
	}
	if tag.RowsAffected() != 1 {
		return "unknown", ErrExecutionFenced
	}
	return "rejected", nil
}

// RetryExecution runs under authority, after current intent/epoch/revision have
// been reviewed by the owning command. The old token is a compare-and-swap
// precondition, never permission. Only one competing retry wins. Commit must be
// acknowledged before dispatching the new token; ambiguity requires lookup.
func RetryExecution(ctx context.Context, tx pgx.Tx, a Action, previousToken, digest string) error {
	if _, err := uuid.Parse(a.ExecutionToken); err != nil || a.ExecutionToken == previousToken {
		return ErrIdentity
	}
	tag, err := tx.Exec(ctx, `UPDATE online_save_actions a SET execution_token=$3,execution_fenced=false,disposition='unknown',response_status=NULL WHERE user_id=$1 AND operation_id=$2 AND execution_token=$4 AND execution_fenced AND intent_hash=$5 AND request_path=$6 AND completed_at IS NULL AND domain_committed_at IS NULL AND NOT EXISTS(SELECT 1 FROM online_save_effects e WHERE e.user_id=a.user_id AND e.operation_id=a.operation_id)`, a.UserID, a.ID, a.ExecutionToken, previousToken, digest, a.Path)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrExecutionFenced
	}
	return nil
}
