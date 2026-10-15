// Package saveaction records online request identity and domain commit evidence.
// Admission is not success, and a receipt never authorizes a domain operation.
package saveaction

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrIdentity = errors.New("action identity belongs to a different intent")

type key struct{}
type Action struct {
	UserID   int64
	ID       string
	Revision string
	Path     string
	// Outcome is request-local rollback evidence from an installed committing
	// boundary, not a substitute for the durable receipt.
	Outcome        *Outcome
	Atomic         bool
	NoChange       bool // the installed owning command, not an incidental nested no-op
	RetryTransient bool // installed atomic command with typed transient failures
}

type Outcome struct{ RolledBack, ReadOnly, RejectedBeforeWrite atomic.Bool }

// RejectBeforeWrite is for an explicit validation/preflight exit before calling
// a mutating service. It must never be used for an uncertain dispatch failure.
func RejectBeforeWrite(ctx context.Context) {
	if a, ok := ctx.Value(key{}).(Action); ok && a.Outcome != nil {
		a.Outcome.RejectedBeforeWrite.Store(true)
	}
}

// ReadOnly marks an explicitly installed query/proposal command, never an
// unaudited mutation. Handler completion can truthfully confirm its no-write
// result without inventing domain audit or an uncertain product effect.
func ReadOnly(ctx context.Context) {
	if a, ok := ctx.Value(key{}).(Action); ok && a.Outcome != nil {
		a.Outcome.ReadOnly.Store(true)
	}
}

// ConfirmNoChange is called only after the domain has checked current authority
// and observed the requested state already holds under its transaction locks.
func ConfirmNoChange(ctx context.Context, tx pgx.Tx) error {
	a, ok := ctx.Value(key{}).(Action)
	if !ok || !a.Atomic || !a.NoChange {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE online_save_actions a SET domain_committed_at=clock_timestamp(),domain_outcome='completed' WHERE user_id=$1 AND operation_id=$2 AND domain_committed_at IS NULL AND completed_at IS NULL AND NOT EXISTS(SELECT 1 FROM online_save_effects e WHERE e.user_id=a.user_id AND e.operation_id=a.operation_id)`, a.UserID, a.ID)
	return err
}

func RolledBack(ctx context.Context) {
	if a, ok := ctx.Value(key{}).(Action); ok && a.Outcome != nil {
		a.Outcome.RolledBack.Store(true)
	}
}

// ConfirmDomain seals the primary command's receipt in its OWN transaction,
// after authority/invariant checks and immutable audit, before commit. Only
// explicitly installed single-transaction form adapters opt into this contract.
// An HTTP dispatch or a read-only transaction cannot produce this confirmation.
func ConfirmDomain(ctx context.Context, tx pgx.Tx) error {
	a, ok := ctx.Value(key{}).(Action)
	if !ok || !a.Atomic {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE online_save_actions a SET domain_committed_at=clock_timestamp() WHERE user_id=$1 AND operation_id=$2 AND domain_committed_at IS NULL AND completed_at IS NULL AND EXISTS(SELECT 1 FROM online_save_effects e WHERE e.user_id=a.user_id AND e.operation_id=a.operation_id)`, a.UserID, a.ID)
	return err
}

func WithAction(ctx context.Context, a Action) context.Context {
	return context.WithValue(ctx, key{}, a)
}

func WithoutAction(ctx context.Context) context.Context {
	return context.WithValue(ctx, key{}, struct{}{})
}

func SubmittedRevision(ctx context.Context) string {
	a, _ := ctx.Value(key{}).(Action)
	return a.Revision
}

func SubmittedOperationID(ctx context.Context) string {
	a, _ := ctx.Value(key{}).(Action)
	return a.ID
}

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// RecordEffect MUST use the effect/audit transaction. Failure aborts that domain
// transaction. This is evidence of that step only, never of an entire HTTP request.
func RecordEffect(ctx context.Context, tx executor, userID int64, action, target string, id int64) error {
	a, ok := ctx.Value(key{}).(Action)
	if !ok {
		return nil
	}
	if a.UserID != userID {
		return errors.New("online action actor mismatch")
	}
	_, err := tx.Exec(ctx, `INSERT INTO online_save_effects(user_id,operation_id,action,target_type,target_id) VALUES($1,$2,$3,$4,$5)`, a.UserID, a.ID, action, target, id)
	return err
}

type Receipt struct {
	RetrySameOperation bool     `json:"retry_same_operation,omitempty"`
	OperationID        string   `json:"operation_id"`
	Disposition        string   `json:"disposition"`
	ReviewURL          string   `json:"review_url"`
	InputAccessURL     string   `json:"input_access_url,omitempty"`
	Location           string   `json:"location,omitempty"`
	CommittedSteps     int      `json:"committed_steps"`
	ResponseStatus     int      `json:"response_status,omitempty"`
	ReviewPath         string   `json:"-"`
	RequestPath        string   `json:"-"`
	RetrySafe          bool     `json:"retry_safe"`
	Effects            []Effect `json:"effects"`
}

type Effect struct {
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   int64  `json:"target_id"`
}

type Store struct{ Pool *pgxpool.Pool }

func (s Store) Admit(ctx context.Context, a Action, digest, method, path, review string) (bool, error) {
	if _, err := uuid.Parse(a.ID); err != nil {
		return false, ErrIdentity
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO online_save_actions(user_id,operation_id,intent_hash,method,request_path,review_path) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, a.UserID, a.ID, digest, method, path, review)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return true, nil
	}
	var old string
	if err = s.Pool.QueryRow(ctx, `SELECT intent_hash FROM online_save_actions WHERE user_id=$1 AND operation_id=$2`, a.UserID, a.ID).Scan(&old); err != nil {
		return false, err
	}
	if old != digest {
		return false, ErrIdentity
	}
	if a.RetryTransient {
		tag, err := s.Pool.Exec(ctx, `UPDATE online_save_actions a SET transient_retry=false,response_status=NULL WHERE user_id=$1 AND operation_id=$2 AND transient_retry AND completed_at IS NULL AND domain_committed_at IS NULL AND NOT EXISTS(SELECT 1 FROM online_save_effects e WHERE e.user_id=a.user_id AND e.operation_id=a.operation_id)`, a.UserID, a.ID)
		return tag.RowsAffected() == 1, err
	}
	return false, nil
}

func (s Store) Receipt(ctx context.Context, a Action) (Receipt, error) {
	r := Receipt{OperationID: a.ID, Disposition: "not_received", ReviewURL: fmt.Sprintf("/save-actions/%s/review", a.ID)}
	if _, err := uuid.Parse(a.ID); err != nil {
		return r, ErrIdentity
	}
	err := s.Pool.QueryRow(ctx, `SELECT CASE WHEN domain_committed_at IS NOT NULL THEN coalesce(domain_outcome,'saved') ELSE disposition END,location,review_path,request_path,coalesce(response_status,0),(SELECT count(*) FROM online_save_effects e WHERE e.user_id=a.user_id AND e.operation_id=a.operation_id),transient_retry AND completed_at IS NULL AND domain_committed_at IS NULL FROM online_save_actions a WHERE user_id=$1 AND operation_id=$2`, a.UserID, a.ID).Scan(&r.Disposition, &r.Location, &r.ReviewPath, &r.RequestPath, &r.ResponseStatus, &r.CommittedSteps, &r.RetrySameOperation)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	r.RetrySafe = r.Disposition == "rejected"
	r.InputAccessURL = fmt.Sprintf("/save-actions/%s/input-access", a.ID)
	rows, err := s.Pool.Query(ctx, `SELECT action,target_type,target_id FROM online_save_effects WHERE user_id=$1 AND operation_id=$2 ORDER BY id`, a.UserID, a.ID)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	r.Effects = []Effect{}
	for rows.Next() {
		var e Effect
		if err := rows.Scan(&e.Action, &e.TargetType, &e.TargetID); err != nil {
			return r, err
		}
		r.Effects = append(r.Effects, e)
	}
	r.CommittedSteps = len(r.Effects)
	return r, rows.Err()
}

// Complete records an observed handler outcome, not a dispatch acknowledgement.
// Unless an installed single-command adapter sealed its domain confirmation, a
// crash before this write leaves unknown. Committed steps alone do not prove
// that later steps in the request completed. HTTP 202 is acceptance, not proof
// that the asynchronously dispatched command finished.
func (s Store) Complete(ctx context.Context, a Action, status int, location string, safeBoundary bool) error {
	rejected := a.Outcome != nil && (safeBoundary && a.Outcome.RolledBack.Load() || a.Outcome.RejectedBeforeWrite.Load())
	if a.RetryTransient && status >= 500 && rejected {
		_, err := s.Pool.Exec(ctx, `UPDATE online_save_actions a SET response_status=$3,transient_retry=true WHERE user_id=$1 AND operation_id=$2 AND completed_at IS NULL AND domain_committed_at IS NULL AND NOT EXISTS(SELECT 1 FROM online_save_effects e WHERE e.user_id=a.user_id AND e.operation_id=a.operation_id)`, a.UserID, a.ID, status)
		return err
	}
	readOnly := a.Outcome != nil && a.Outcome.ReadOnly.Load()
	_, err := s.Pool.Exec(ctx, `UPDATE online_save_actions a SET response_status=$3,location=$4,completed_at=now(),disposition=CASE WHEN $3>=200 AND $3<400 AND $3<>202 AND EXISTS(SELECT 1 FROM online_save_effects e WHERE e.user_id=a.user_id AND e.operation_id=a.operation_id) THEN 'saved' WHEN $3>=200 AND $3<400 AND $3<>202 AND $6 THEN 'completed' WHEN $3>=400 AND ($5 OR $6) AND NOT EXISTS(SELECT 1 FROM online_save_effects e WHERE e.user_id=a.user_id AND e.operation_id=a.operation_id) THEN 'rejected' ELSE 'review' END WHERE user_id=$1 AND operation_id=$2 AND completed_at IS NULL`, a.UserID, a.ID, status, location, rejected, readOnly)
	return err
}
