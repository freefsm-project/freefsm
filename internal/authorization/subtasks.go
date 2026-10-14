package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrSubtaskInvalid = errors.New("invalid Subtask intent")
var ErrSubtaskReview = errors.New("legacy checklist requires Administrator review")

// Subtask identity and completion revision are independent of position, title,
// other entries, and the Job's details or Status revision.
type Subtask struct {
	ID                 string     `json:"id"`
	Title              string     `json:"title"`
	SortOrder          int        `json:"sort_order"`
	Completed          bool       `json:"completed"`
	CompletionRevision string     `json:"completion_revision"`
	CompletionActor    *int64     `json:"completion_actor,omitempty"`
	CompletionAt       *time.Time `json:"completion_at,omitempty"`
	RemovedAt          *time.Time `json:"removed_at,omitempty"`
}
type SubtaskCommand struct {
	OperationID        string     `json:"operation_id"`
	Action             string     `json:"action"`
	SubtaskID          string     `json:"subtask_id,omitempty"`
	Title              string     `json:"title,omitempty"`
	CompletionRevision string     `json:"completion_revision,omitempty"`
	Order              []string   `json:"order,omitempty"`
	OccurredAt         *time.Time `json:"occurred_at,omitempty"`
}
type SubtaskReceipt struct {
	OperationID string    `json:"operation_id"`
	SubtaskID   string    `json:"subtask_id,omitempty"`
	Code        string    `json:"code"`
	AcceptedAt  time.Time `json:"accepted_at"`
}
type SubtaskHistory struct {
	ID         int64           `json:"id"`
	SubtaskID  *string         `json:"subtask_id,omitempty"`
	ActorID    int64           `json:"actor_id"`
	AcceptedAt time.Time       `json:"accepted_at"`
	Action     string          `json:"action"`
	Before     json.RawMessage `json:"before"`
	After      json.RawMessage `json:"after"`
}
type Checklist struct {
	Entries        []Subtask        `json:"subtasks"`
	History        []SubtaskHistory `json:"history"`
	ReviewRequired bool             `json:"review_required"`
}

func subtaskRows(ctx context.Context, tx pgx.Tx, jobID int64) ([]Subtask, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,title,sort_order,completed,completion_revision::text,completion_actor,completion_at,removed_at FROM job_subtasks WHERE job_id=$1 ORDER BY sort_order,legacy_position NULLS LAST,id`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Subtask{}
	for rows.Next() {
		var v Subtask
		if err := rows.Scan(&v.ID, &v.Title, &v.SortOrder, &v.Completed, &v.CompletionRevision, &v.CompletionActor, &v.CompletionAt, &v.RemovedAt); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *Service) Subtasks(ctx context.Context, a Actor, jobID int64) (Checklist, error) {
	result := Checklist{Entries: []Subtask{}, History: []SubtaskHistory{}}
	err := s.WithinParent(ctx, a, "job", jobID, "", func(tx pgx.Tx, p Principal) error {
		if err := subtaskInventory(ctx, tx, jobID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subtask_legacy_reviews WHERE job_id=$1 AND reviewed_at IS NULL)`, jobID).Scan(&result.ReviewRequired); err != nil {
			return err
		}
		all, err := subtaskRows(ctx, tx, jobID)
		if err != nil {
			return err
		}
		for _, v := range all {
			if v.RemovedAt == nil {
				result.Entries = append(result.Entries, v)
			}
		}
		rows, err := tx.Query(ctx, `SELECT id,subtask_id::text,actor_id,accepted_at,action,before_value,after_value FROM subtask_history WHERE job_id=$1 ORDER BY id`, jobID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v SubtaskHistory
			if err := rows.Scan(&v.ID, &v.SubtaskID, &v.ActorID, &v.AcceptedAt, &v.Action, &v.Before, &v.After); err != nil {
				return err
			}
			result.History = append(result.History, v)
		}
		return rows.Err()
	})
	return result, err
}

func subtaskCode(err error) string {
	switch {
	case err == nil:
		return "accepted"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, ErrConflict):
		return "subtask_conflict"
	case errors.Is(err, ErrSubtaskInvalid):
		return "invalid_subtask"
	case errors.Is(err, ErrSubtaskReview):
		return "review_required"
	}
	return ""
}
func subtaskError(code string) error {
	switch code {
	case "not_found":
		return ErrNotFound
	case "forbidden":
		return ErrForbidden
	case "subtask_conflict":
		return ErrConflict
	case "invalid_subtask":
		return ErrSubtaskInvalid
	case "review_required":
		return ErrSubtaskReview
	}
	return nil
}

// SubmitSubtask commits one immutable intent, effect, history, audit and receipt.
// Terminal receipts expose identity/disposition only, including after scope loss.
func (s *Service) SubmitSubtask(ctx context.Context, a Actor, jobID int64, in SubtaskCommand) (SubtaskReceipt, error) {
	result := SubtaskReceipt{OperationID: in.OperationID}
	op, err := uuid.Parse(in.OperationID)
	if err != nil || op == uuid.Nil {
		return result, ErrSubtaskInvalid
	}
	in.OperationID = op.String()
	result.OperationID = in.OperationID
	encoded, _ := json.Marshal(struct {
		Job    int64
		Intent SubtaskCommand
	}{jobID, in})
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	var outcome error
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var saved, client string
		err := tx.QueryRow(ctx, `SELECT intent_hash,client_key,coalesce(subtask_id::text,''),code,accepted_at FROM subtask_operation_receipts WHERE user_id=$1 AND operation_id=$2`, a.UserID, in.OperationID).Scan(&saved, &client, &result.SubtaskID, &result.Code, &result.AcceptedAt)
		if err == nil {
			if saved != digest || client != a.Channel+":"+a.SessionHash {
				return ErrConflict
			}
			outcome = subtaskError(result.Code)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		result.SubtaskID, outcome = applySubtask(ctx, tx, p, a, jobID, in)
		result.Code = subtaskCode(outcome)
		if result.Code == "" {
			return outcome
		}
		if outcome != nil {
			result.SubtaskID = ""
		}
		return tx.QueryRow(ctx, `INSERT INTO subtask_operation_receipts(user_id,operation_id,client_key,intent_hash,subtask_id,code) VALUES($1,$2,$3,$4,nullif($5,'')::uuid,$6) RETURNING accepted_at`, a.UserID, in.OperationID, a.Channel+":"+a.SessionHash, digest, result.SubtaskID, result.Code).Scan(&result.AcceptedAt)
	})
	if err != nil {
		return SubtaskReceipt{}, err
	}
	return result, outcome
}

func (s *Service) SubtaskReceipt(ctx context.Context, a Actor, operation string) (SubtaskReceipt, error) {
	result := SubtaskReceipt{OperationID: operation}
	op, err := uuid.Parse(operation)
	if err != nil {
		return result, ErrNotFound
	}
	result.OperationID = op.String()
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		err := tx.QueryRow(ctx, `SELECT coalesce(subtask_id::text,''),code,accepted_at FROM subtask_operation_receipts WHERE user_id=$1 AND operation_id=$2 AND client_key=$3`, a.UserID, result.OperationID, a.Channel+":"+a.SessionHash).Scan(&result.SubtaskID, &result.Code, &result.AcceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return result, err
}

func applySubtask(ctx context.Context, tx pgx.Tx, p Principal, a Actor, jobID int64, in SubtaskCommand) (string, error) {
	if in.OccurredAt != nil && in.OccurredAt.After(time.Now()) {
		return "", ErrSubtaskInvalid
	}
	permission := "job.subtask.manage"
	switch in.Action {
	case "complete":
		permission = "job.subtask.complete"
	case "reopen":
		permission = "job.subtask.reopen"
	case "add", "rename", "remove", "reorder":
	default:
		return "", ErrSubtaskInvalid
	}
	if a.Channel == "api" && permission == "job.subtask.manage" {
		return "", ErrForbidden
	}
	scope, err := parentAccess(ctx, tx, p, "job", jobID, permission, true)
	if err != nil {
		return "", err
	}
	if err := subtaskInventory(ctx, tx, jobID); err != nil {
		return "", err
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subtask_legacy_reviews WHERE job_id=$1 AND reviewed_at IS NULL)`, jobID).Scan(&pending); err != nil {
		return "", err
	}
	if pending {
		return "", ErrSubtaskReview
	}
	all, err := subtaskRows(ctx, tx, jobID)
	if err != nil {
		return "", err
	}
	var before *Subtask
	active := map[string]Subtask{}
	for _, v := range all {
		if v.RemovedAt == nil {
			active[v.ID] = v
			if v.ID == in.SubtaskID {
				copy := v
				before = &copy
			}
		}
	}
	if in.Action != "add" && in.Action != "reorder" && before == nil {
		return "", ErrNotFound
	}
	var after any
	id := in.SubtaskID
	switch in.Action {
	case "add":
		if strings.TrimSpace(in.Title) == "" || in.SubtaskID != "" {
			return "", ErrSubtaskInvalid
		}
		id = uuid.NewString()
		nextOrder := 0
		for _, v := range all {
			if v.SortOrder >= nextOrder {
				nextOrder = v.SortOrder + 1
			}
		}
		v := Subtask{ID: id, Title: in.Title, SortOrder: nextOrder, CompletionRevision: uuid.NewString()}
		_, err = tx.Exec(ctx, `INSERT INTO job_subtasks(id,job_id,title,sort_order,completion_revision) VALUES($1,$2,$3,$4,$5)`, v.ID, jobID, v.Title, v.SortOrder, v.CompletionRevision)
		after = v
	case "rename":
		if strings.TrimSpace(in.Title) == "" {
			return "", ErrSubtaskInvalid
		}
		if before.Title == in.Title {
			return id, nil
		}
		v := *before
		v.Title = in.Title
		_, err = tx.Exec(ctx, `UPDATE job_subtasks SET title=$2 WHERE id=$1`, id, in.Title)
		after = v
	case "complete", "reopen":
		if _, e := uuid.Parse(in.CompletionRevision); e != nil {
			return "", ErrSubtaskInvalid
		}
		completed := in.Action == "complete"
		if before.Completed == completed {
			return id, nil
		}
		if before.CompletionRevision != in.CompletionRevision {
			return "", ErrConflict
		}
		v := *before
		v.Completed = completed
		v.CompletionRevision = uuid.NewString()
		v.CompletionActor = &a.UserID
		err = tx.QueryRow(ctx, `UPDATE job_subtasks SET completed=$2,completion_revision=$3,completion_actor=$4,completion_at=clock_timestamp() WHERE id=$1 RETURNING completion_at`, id, completed, v.CompletionRevision, a.UserID).Scan(&v.CompletionAt)
		after = v
	case "remove":
		v := *before
		err = tx.QueryRow(ctx, `UPDATE job_subtasks SET removed_at=clock_timestamp() WHERE id=$1 RETURNING removed_at`, id).Scan(&v.RemovedAt)
		after = v
	case "reorder":
		if len(in.Order) != len(active) {
			return "", ErrConflict
		}
		seen := map[string]bool{}
		for _, id := range in.Order {
			if _, ok := active[id]; !ok || seen[id] {
				return "", ErrSubtaskInvalid
			}
			seen[id] = true
		}
		for i, id := range in.Order {
			if _, err = tx.Exec(ctx, `UPDATE job_subtasks SET sort_order=$2 WHERE id=$1`, id, i); err != nil {
				return "", err
			}
		}
		after = in.Order
	}
	if err != nil {
		return "", err
	}
	b, _ := json.Marshal(before)
	next, _ := json.Marshal(after)
	if in.Action == "reorder" {
		b, _ = json.Marshal(all)
	}
	_, err = tx.Exec(ctx, `INSERT INTO subtask_history(job_id,subtask_id,actor_id,action,before_value,after_value,operation_id) VALUES($1,nullif($2,'')::uuid,$3,$4,$5,$6,$7)`, jobID, id, a.UserID, in.Action, b, next, in.OperationID)
	if err != nil {
		return "", err
	}
	if err = audit(ctx, tx, a, p, permission, scope, "job", jobID, map[string]any{"subtask_id": id, "operation_id": in.OperationID, "occurred_at": in.OccurredAt, "before": json.RawMessage(b), "after": json.RawMessage(next)}); err != nil {
		return "", err
	}
	return id, projectSubtasks(ctx, tx, jobID)
}

func projectSubtasks(ctx context.Context, tx pgx.Tx, jobID int64) error {
	all, err := subtaskRows(ctx, tx, jobID)
	if err != nil {
		return err
	}
	active := []Subtask{}
	for _, v := range all {
		if v.RemovedAt == nil {
			active = append(active, v)
		}
	}
	b, _ := json.Marshal(active)
	_, err = tx.Exec(ctx, `UPDATE jobs SET subtasks=$2,updated_at=clock_timestamp() WHERE id=$1`, jobID, b)
	return err
}
