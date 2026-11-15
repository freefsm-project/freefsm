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

// RequireExecution is the mandatory boundary for execution-owning commands.
func RequireExecution(ctx context.Context, userID int64, path string) (Action, error) {
	a, ok := ctx.Value(key{}).(Action)
	if !ok || a.UserID != userID || a.Path != path || !a.Atomic || !a.NoChange || a.IntentHash == "" || a.Revision == "" {
		return Action{}, ErrIdentity
	}
	for _, value := range []string{a.ID, a.ExecutionToken} {
		id, err := uuid.Parse(value)
		if err != nil || id == uuid.Nil {
			return Action{}, ErrIdentity
		}
	}
	return a, nil
}

// MatchExecution binds the trusted dispatch metadata to its admitted intent.
// The authority and action row locks must already be held by the owner.
func MatchExecution(ctx context.Context, tx pgx.Tx, a Action) error {
	var matches bool
	err := tx.QueryRow(ctx, `SELECT intent_hash=$3 AND method='POST' AND request_path=$4 AND execution_token=$5 AND execution_review=$6 FROM online_save_actions WHERE user_id=$1 AND operation_id=$2`, a.UserID, a.ID, a.IntentHash, a.Path, a.ExecutionToken, a.Revision).Scan(&matches)
	if err != nil {
		return err
	}
	if !matches {
		return ErrIdentity
	}
	return nil
}

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
	tag, err := s.Pool.Exec(ctx, `INSERT INTO online_save_actions(user_id,operation_id,intent_hash,method,request_path,review_path,execution_token,execution_review) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, a.UserID, a.ID, digest, method, path, review, a.ExecutionToken, a.Revision)
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
// A present identity is checked even when an adapter loses its token. Generic
// callers without an Action remain supported; execution-owning commands must
// separately require Action/token at their entry point before calling services.
func LockExecution(ctx context.Context, tx pgx.Tx) error {
	a, ok := ctx.Value(key{}).(Action)
	if !ok {
		return nil
	}
	var owned bool
	var executable bool
	err := tx.QueryRow(ctx, `SELECT execution_token IS NOT NULL,coalesce(execution_token=nullif($4,'')::uuid AND NOT execution_fenced AND domain_committed_at IS NULL AND completed_at IS NULL AND request_path=$3,false) FROM online_save_actions WHERE user_id=$1 AND operation_id=$2 FOR UPDATE`, a.UserID, a.ID, a.Path, a.ExecutionToken).Scan(&owned, &executable)
	if errors.Is(err, pgx.ErrNoRows) && a.ExecutionToken == "" {
		return nil // preserve generic commands without admitted receipt history
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrExecutionFenced
	}
	if err != nil {
		return err
	}
	if !owned && a.ExecutionToken == "" {
		return nil // legacy rows never acquire invented execution ownership
	}
	if !executable {
		return ErrExecutionFenced
	}
	if !a.Atomic {
		return ErrIdentity
	}
	return nil
}

// FenceExecution is called by the owning command's terminal reconciliation
// transaction AFTER acquiring authority. Missing history stays unknown. A
// committed result stays committed. The caller must inspect current domain
// state under its locks before committing this nonexecution decision.
// No retry is granted here; RetryExecution requires an explicit reviewed retry.
func FenceExecution(ctx context.Context, tx pgx.Tx, userID int64, id, path string) (string, error) {
	var token *string
	var committed bool
	var outcome string
	err := tx.QueryRow(ctx, `SELECT execution_token,domain_committed_at IS NOT NULL,coalesce(domain_outcome,'saved') FROM online_save_actions WHERE user_id=$1 AND operation_id=$2 AND request_path=$3 FOR UPDATE`, userID, id, path).Scan(&token, &committed, &outcome)
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
		return outcome, nil
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
