package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type JobExecutionState struct {
	Notes          string `json:"field_notes"`
	NotesRevision  string `json:"notes_revision"`
	ReviewRequired bool   `json:"review_required"`
	CanEditNotes   bool   `json:"can_edit_notes"`
	CanReview      bool   `json:"can_review"`
}

type JobExecutionCommand struct {
	MissingNotes    bool       `json:"missing_notes,omitempty"`
	OperationID     string     `json:"operation_id"`
	NotesRevision   string     `json:"notes_revision,omitempty"`
	Notes           string     `json:"field_notes,omitempty"`
	OccurredAt      *time.Time `json:"occurred_at,omitempty"`
	StatusID        int64      `json:"status_id,omitempty"`
	StatusRevision  string     `json:"status_revision,omitempty"`
	MeaningRevision string     `json:"meaning_revision,omitempty"`
}

type JobExecutionReceipt struct {
	OperationID string    `json:"operation_id"`
	Code        string    `json:"code"`
	EffectID    string    `json:"effect_id,omitempty"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

type JobExecutionConflict struct{ Code string }

func (e *JobExecutionConflict) Error() string { return e.Code }

func (s *Service) JobExecution(ctx context.Context, a Actor, id int64) (JobExecutionState, error) {
	var out JobExecutionState
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, "job", id, "", true); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT coalesce(tech_notes,''),notes_revision::text,EXISTS(SELECT 1 FROM job_notes_reviews WHERE job_id=$1 AND reviewed_at IS NULL) FROM jobs WHERE id=$1`, id).Scan(&out.Notes, &out.NotesRevision, &out.ReviewRequired)
		if err != nil {
			return err
		}
		_, err = parentAccess(ctx, tx, p, "job", id, "job.field_notes.edit", true)
		if err != nil && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
			return err
		}
		out.CanEditNotes = err == nil && !out.ReviewRequired
		out.CanReview = out.ReviewRequired && p.Protected && a.Channel != "api"
		return nil
	})
	return out, err
}

func executionCode(err error) string {
	if err == nil {
		return "accepted"
	}
	var conflict *JobExecutionConflict
	if errors.As(err, &conflict) {
		return conflict.Code
	}
	if errors.Is(err, ErrNotFound) {
		return "not_found"
	}
	if errors.Is(err, ErrForbidden) {
		return "forbidden"
	}
	return ""
}
func executionError(code string) error {
	switch code {
	case "accepted":
		return nil
	case "not_found":
		return ErrNotFound
	case "forbidden":
		return ErrForbidden
	default:
		return &JobExecutionConflict{code}
	}
}

// SubmitJobNotes fences current authority and commits effect, history, audit and
// terminal receipt together. Transient failures leave the original ID retryable.
func (s *Service) SubmitJobNotes(ctx context.Context, a Actor, id int64, in JobExecutionCommand) (JobExecutionReceipt, error) {
	return s.submitJobExecution(ctx, a, id, "field_notes", in)
}

func (s *Service) SubmitJobStatus(ctx context.Context, a Actor, id int64, in JobExecutionCommand) (JobExecutionReceipt, error) {
	return s.submitJobExecution(ctx, a, id, "status", in)
}

func (s *Service) submitJobExecution(ctx context.Context, a Actor, id int64, kind string, in JobExecutionCommand) (JobExecutionReceipt, error) {
	return s.submitJobExecutionBound(ctx, a, id, kind, in, nil)
}

func (s *Service) submitJobExecutionBound(ctx context.Context, a Actor, id int64, kind string, in JobExecutionCommand, batch *batchExecution) (JobExecutionReceipt, error) {
	out := JobExecutionReceipt{OperationID: in.OperationID}
	op, err := uuid.Parse(in.OperationID)
	if err != nil || op == uuid.Nil {
		permission := "job.status"
		if kind == "field_notes" {
			permission = "job.field_notes.edit"
		}
		if denied := s.ParentAccess(ctx, a, "job", id, permission); denied != nil {
			return out, denied
		}
		return out, &JobExecutionConflict{"invalid_operation_id"}
	}
	in.OperationID = op.String()
	out.OperationID = in.OperationID
	var binding *BatchNotesOperation
	if batch != nil {
		binding = &batch.operation
	}
	encoded, _ := json.Marshal(struct {
		JobID  int64
		Kind   string
		Intent JobExecutionCommand
		Batch  *BatchNotesOperation `json:",omitempty"`
	}{id, kind, in, binding})
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	var outcome error
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := admitOperation(ctx, tx, a, in.OperationID, "job.execution", digest); err != nil {
			if errors.Is(err, ErrConflict) {
				return &JobExecutionConflict{"operation_intent_changed"}
			}
			return err
		}
		var saved, client string
		err := tx.QueryRow(ctx, `SELECT intent_hash,client_key,code,coalesce(effect_id::text,''),accepted_at FROM job_execution_receipts WHERE user_id=$1 AND operation_id=$2`, a.UserID, in.OperationID).Scan(&saved, &client, &out.Code, &out.EffectID, &out.AcceptedAt)
		if err == nil {
			if saved != digest || client != a.Channel+":"+a.SessionHash {
				return &JobExecutionConflict{"operation_intent_changed"}
			}
			outcome = executionError(out.Code)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if batch != nil {
			outcome = batch.validate(ctx, tx, a)
		}
		// A dependency rejection is terminal intent, never a domain effect.
		if outcome == nil {
			if kind == "status" {
				out.EffectID, outcome = applyJobStatus(ctx, tx, p, a, id, in)
			} else {
				out.EffectID, outcome = applyJobNotes(ctx, tx, p, a, id, in)
			}
		}
		out.Code = executionCode(outcome)
		if out.Code == "" {
			return outcome
		}
		return tx.QueryRow(ctx, `INSERT INTO job_execution_receipts(user_id,operation_id,client_key,intent_hash,code,effect_id) VALUES($1,$2,$3,$4,$5,nullif($6,'')::uuid) RETURNING accepted_at`, a.UserID, in.OperationID, a.Channel+":"+a.SessionHash, digest, out.Code, out.EffectID).Scan(&out.AcceptedAt)
	})
	if err != nil {
		return JobExecutionReceipt{}, err
	}
	if out.Code == "forbidden" || out.Code == "not_found" {
		slog.WarnContext(ctx, "Job execution denied", "actor_id", a.UserID, "channel", a.Channel, "facet", kind, "outcome", out.Code)
	}
	return out, outcome
}

func applyJobNotes(ctx context.Context, tx pgx.Tx, p Principal, a Actor, id int64, in JobExecutionCommand) (string, error) {
	if in.MissingNotes {
		return "", &JobExecutionConflict{"invalid_intent"}
	}
	scope, err := parentAccess(ctx, tx, p, "job", id, "job.field_notes.edit", true)
	if err != nil {
		return "", err
	}
	if in.StatusID != 0 || in.StatusRevision != "" || in.MeaningRevision != "" {
		return "", &JobExecutionConflict{"invalid_intent"}
	}
	var old, revision string
	var review bool
	if err = tx.QueryRow(ctx, `SELECT coalesce(tech_notes,''),notes_revision::text,EXISTS(SELECT 1 FROM job_notes_reviews WHERE job_id=$1 AND reviewed_at IS NULL) FROM jobs WHERE id=$1`, id).Scan(&old, &revision, &review); err != nil {
		return "", err
	}
	if review {
		return "", &JobExecutionConflict{"legacy_review_required"}
	}
	if in.NotesRevision != revision {
		return "", &JobExecutionConflict{"stale_notes_revision"}
	}
	if in.OccurredAt != nil && in.OccurredAt.After(time.Now()) {
		return "", &JobExecutionConflict{"invalid_occurrence"}
	}
	if old == in.Notes {
		return revision, nil
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.field_notes','allowed',true)`); err != nil {
		return "", err
	}
	if err = tx.QueryRow(ctx, `UPDATE jobs SET tech_notes=$2,updated_at=clock_timestamp() WHERE id=$1 RETURNING notes_revision::text`, id, in.Notes).Scan(&revision); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO job_notes_history(revision,job_id,content,actor_id,accepted_at) VALUES($1,$2,$3,$4,clock_timestamp())`, revision, id, in.Notes, a.UserID); err != nil {
		return "", err
	}
	err = audit(ctx, tx, a, p, "job.field_notes.edit", scope, "job", id, map[string]any{"before": old, "after": in.Notes, "revision": revision, "operation_id": in.OperationID, "occurred_at": in.OccurredAt})
	return revision, err
}

func applyJobStatus(ctx context.Context, tx pgx.Tx, p Principal, a Actor, id int64, in JobExecutionCommand) (string, error) {
	if in.StatusID <= 0 {
		return "", &JobExecutionConflict{"invalid_intent"}
	}
	scope, err := parentAccess(ctx, tx, p, "job", id, "job.status", true)
	if err != nil {
		return "", err
	}
	if in.Notes != "" || in.NotesRevision != "" {
		return "", &JobExecutionConflict{"invalid_intent"}
	}
	if in.OccurredAt != nil && in.OccurredAt.After(time.Now()) {
		return "", &JobExecutionConflict{"invalid_occurrence"}
	}
	var current *int64
	var revision, meaning, category string
	err = tx.QueryRow(ctx, `SELECT j.status_id,j.status_revision::text,s.meaning_revision::text,s.category_key FROM jobs j JOIN statuses s ON s.id=$2 JOIN status_workflows w ON w.id=s.workflow_id AND w.object_type='job' AND w.company_id=j.company_id WHERE j.id=$1 AND s.company_id=j.company_id FOR UPDATE OF j,s`, id, in.StatusID).Scan(&current, &revision, &meaning, &category)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", &JobExecutionConflict{"status_reselection_required"}
	}
	if err != nil {
		return "", err
	}
	if in.StatusRevision == "" || meaning != in.MeaningRevision {
		return "", &JobExecutionConflict{"status_reselection_required"}
	}
	if current != nil && *current == in.StatusID {
		return revision, nil
	}
	if revision != in.StatusRevision {
		return "", &JobExecutionConflict{"status_reselection_required"}
	}
	if err = JobStatusDependencies(ctx, tx, p.CompanyID, id, category); err != nil {
		if errors.Is(err, ErrConflict) {
			return "", &JobExecutionConflict{"job_dependency_conflict"}
		}
		return "", err
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`); err != nil {
		return "", err
	}
	if err = tx.QueryRow(ctx, `UPDATE jobs SET status_id=$2,updated_at=clock_timestamp() WHERE id=$1 RETURNING status_revision::text`, id, in.StatusID).Scan(&revision); err != nil {
		return "", err
	}
	detail := map[string]any{"from_status_id": current, "to_status_id": in.StatusID, "category_key": category, "revision": revision, "operation_id": in.OperationID, "occurred_at": in.OccurredAt}
	metadata, err := json.Marshal(detail)
	if err != nil {
		return "", err
	}
	// Preserve the existing operational Status activity while committing the
	// protected permission audit and new receipt in this same transaction.
	if _, err = tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at) VALUES($1,$2,'status_transitioned','job',$3,$4,clock_timestamp())`, p.CompanyID, a.UserID, id, metadata); err != nil {
		return "", err
	}
	err = audit(ctx, tx, a, p, "job.status", scope, "job", id, detail)
	return revision, err
}

// Receipts deliberately contain no target content or comparison. Current
// resource access is not a prerequisite for reconciliation of terminal intent.
func (s *Service) JobExecutionReceipt(ctx context.Context, a Actor, operation string) (JobExecutionReceipt, error) {
	out := JobExecutionReceipt{OperationID: operation}
	parsed, err := uuid.Parse(operation)
	if err != nil || parsed == uuid.Nil {
		return out, ErrNotFound
	}
	operation = parsed.String()
	out.OperationID = operation
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		err := tx.QueryRow(ctx, `SELECT code,coalesce(effect_id::text,''),accepted_at FROM job_execution_receipts WHERE user_id=$1 AND operation_id=$2`, a.UserID, operation).Scan(&out.Code, &out.EffectID, &out.AcceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}

type JobNotesRevision struct {
	Revision       string     `json:"revision"`
	Content        string     `json:"content"`
	ActorID        *int64     `json:"actor_id"`
	ActorName      string     `json:"actor_name"`
	AcceptedAt     *time.Time `json:"accepted_at"`
	RecordedAt     time.Time  `json:"recorded_at"`
	Baseline       bool       `json:"baseline"`
	BaselineSource string     `json:"baseline_source,omitempty"`
}

func (s *Service) JobNotesHistory(ctx context.Context, a Actor, id int64) ([]JobNotesRevision, error) {
	out := []JobNotesRevision{}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, "job", id, "", true); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT h.revision::text,h.content,h.actor_id,coalesce(u.name,''),h.accepted_at,h.recorded_at,h.baseline,h.baseline_source FROM job_notes_history h LEFT JOIN users u ON u.id=h.actor_id WHERE h.job_id=$1 ORDER BY h.recorded_at,h.revision`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v JobNotesRevision
			if err = rows.Scan(&v.Revision, &v.Content, &v.ActorID, &v.ActorName, &v.AcceptedAt, &v.RecordedAt, &v.Baseline, &v.BaselineSource); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// Review acknowledges an unknown baseline without changing its content, author
// or dates. Only an authenticated protected Administrator may resolve this gate.
func (s *Service) ReviewJobNotes(ctx context.Context, a Actor, id int64, revision, reason string) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Protected || a.Channel == "api" {
			return ErrForbidden
		}
		if _, err := parentAccess(ctx, tx, p, "job", id, "", true); err != nil {
			return err
		}
		if strings.TrimSpace(reason) == "" {
			return &JobExecutionConflict{"review_reason_required"}
		}
		result, err := tx.Exec(ctx, `UPDATE job_notes_reviews SET reviewer_id=$3,reviewed_at=clock_timestamp(),reason=$4 WHERE job_id=$1 AND baseline_revision::text=$2 AND reviewed_at IS NULL`, id, revision, a.UserID, reason)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return &JobExecutionConflict{"legacy_review_changed"}
		}
		return audit(ctx, tx, a, p, "job.field_notes.review", Sealed, "job", id, map[string]any{"baseline_revision": revision, "reason": reason, "historical_attribution": "unknown"})
	})
}
