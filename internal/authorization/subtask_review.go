package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SubtaskReview struct {
	Recorded json.RawMessage `json:"recorded"`
	Snapshot string          `json:"snapshot"`
	Pending  bool            `json:"pending"`
}

// Retain late-discovered legacy data too (for example a restored or imported
// pre-cutover Job). Never replace a recorded array with an empty canonical list.
func subtaskInventory(ctx context.Context, tx pgx.Tx, jobID int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO subtask_legacy_reviews(job_id,recorded) SELECT id,subtasks FROM jobs WHERE id=$1 AND subtasks<>'[]'::jsonb AND NOT EXISTS(SELECT 1 FROM job_subtasks WHERE job_id=$1) ON CONFLICT(job_id) DO NOTHING`, jobID)
	return err
}

func (s *Service) SubtaskReview(ctx context.Context, a Actor, jobID int64) (SubtaskReview, error) {
	var result SubtaskReview
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Protected || a.Channel == "api" {
			return ErrForbidden
		}
		if _, err := parentAccess(ctx, tx, p, "job", jobID, "", true); err != nil {
			return err
		}
		if err := subtaskInventory(ctx, tx, jobID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `SELECT recorded,reviewed_at IS NULL FROM subtask_legacy_reviews WHERE job_id=$1`, jobID).Scan(&result.Recorded, &result.Pending)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		result.Snapshot = fmt.Sprintf("%x", sha256.Sum256(result.Recorded))
		return nil
	})
	return result, err
}

// Review adopts only the recorded present state. It creates no historical
// completion events and leaves unknown completion actor/time NULL. Ordinality
// maps the evidenced legacy row, never an ongoing Subtask identity.
func (s *Service) ReviewSubtasks(ctx context.Context, a Actor, jobID int64, snapshot string, acknowledge bool) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Protected || a.Channel == "api" {
			return ErrForbidden
		}
		if _, err := parentAccess(ctx, tx, p, "job", jobID, "", true); err != nil {
			return err
		}
		if !acknowledge {
			return ErrSubtaskInvalid
		}
		if err := subtaskInventory(ctx, tx, jobID); err != nil {
			return err
		}
		var raw json.RawMessage
		var pending, current bool
		err := tx.QueryRow(ctx, `SELECT r.recorded,r.reviewed_at IS NULL,j.subtasks=r.recorded FROM subtask_legacy_reviews r JOIN jobs j ON j.id=r.job_id WHERE job_id=$1 FOR UPDATE OF r`, jobID).Scan(&raw, &pending, &current)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !pending {
			return nil
		}
		if !current {
			return ErrConflict
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != snapshot {
			return ErrConflict
		}
		var rows []map[string]json.RawMessage
		if json.Unmarshal(raw, &rows) != nil || rows == nil {
			return ErrSubtaskInvalid
		}
		tasks := []Subtask{}
		for _, row := range rows {
			// Existing unexpected identities/references require a separately
			// evidenced mapping, never a guessed interpretation or discarded data.
			for key := range row {
				if key != "title" && key != "sort_order" && key != "completed" {
					return ErrSubtaskReview
				}
			}
			var task Subtask
			if json.Unmarshal(row["title"], &task.Title) != nil || strings.TrimSpace(task.Title) == "" || json.Unmarshal(row["completed"], &task.Completed) != nil {
				return ErrSubtaskInvalid
			}
			if value, ok := row["sort_order"]; ok {
				if json.Unmarshal(value, &task.SortOrder) != nil {
					return ErrSubtaskInvalid
				}
			}
			task.ID = uuid.NewString()
			task.CompletionRevision = uuid.NewString()
			tasks = append(tasks, task)
		}
		mapping := []map[string]any{}
		for i, task := range tasks {
			_, err := tx.Exec(ctx, `INSERT INTO job_subtasks(id,job_id,title,sort_order,completed,completion_revision,legacy_position) VALUES($1,$2,$3,$4,$5,$6,$7)`, task.ID, jobID, task.Title, task.SortOrder, task.Completed, task.CompletionRevision, i)
			if err != nil {
				return err
			}
			mapping = append(mapping, map[string]any{"legacy_position": i, "subtask_id": task.ID})
		}
		encoded, _ := json.Marshal(mapping)
		if _, err := tx.Exec(ctx, `UPDATE subtask_legacy_reviews SET reviewed_by=$2,reviewed_at=clock_timestamp(),mapping=$3 WHERE job_id=$1`, jobID, a.UserID, encoded); err != nil {
			return err
		}
		if err := audit(ctx, tx, a, p, "job.subtask.manage", All, "job", jobID, map[string]any{"legacy_review": true, "unknown_attribution_acknowledged": true, "recorded": raw, "mapping": mapping}); err != nil {
			return err
		}
		return projectSubtasks(ctx, tx, jobID)
	})
}

// SaveChecklistPlanning bridges the existing atomic details-save boundary. The
// client must retain every existing identity; completion is never writable by
// planning. Completion drift on retained rows is ignored, not overwritten.
func (s *Service) SaveChecklistPlanning(ctx context.Context, tx pgx.Tx, a Actor, jobID int64, input []Subtask) error {
	input = append([]Subtask(nil), input...)
	sort.SliceStable(input, func(i, j int) bool { return input[i].SortOrder < input[j].SortOrder })
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	if _, err := parentAccess(ctx, tx, p, "job", jobID, "job.subtask.manage", true); err != nil {
		return err
	}
	if err := subtaskInventory(ctx, tx, jobID); err != nil {
		return err
	}
	var pending bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subtask_legacy_reviews WHERE job_id=$1 AND reviewed_at IS NULL)`, jobID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return ErrSubtaskReview
	}
	old, err := subtaskRows(ctx, tx, jobID)
	if err != nil {
		return err
	}
	active := map[string]Subtask{}
	for _, v := range old {
		if v.RemovedAt == nil {
			active[v.ID] = v
		}
	}
	seen := map[string]bool{}
	for _, v := range input {
		if strings.TrimSpace(v.Title) == "" {
			return ErrSubtaskInvalid
		}
		if v.ID == "" {
			if v.Completed {
				return ErrSubtaskInvalid
			}
			continue
		}
		if _, ok := active[v.ID]; !ok || seen[v.ID] {
			return ErrConflict
		}
		if v.CompletionRevision == active[v.ID].CompletionRevision && v.Completed != active[v.ID].Completed {
			return ErrSubtaskInvalid
		}
		seen[v.ID] = true
	}
	order := []string{}
	for _, v := range input {
		action := "rename"
		if v.ID == "" {
			action = "add"
		}
		id, err := applySubtask(ctx, tx, p, a, jobID, SubtaskCommand{OperationID: uuid.NewString(), Action: action, SubtaskID: v.ID, Title: v.Title})
		if err != nil {
			return err
		}
		order = append(order, id)
	}
	for _, v := range old {
		if v.RemovedAt == nil && !seen[v.ID] {
			if _, err := applySubtask(ctx, tx, p, a, jobID, SubtaskCommand{OperationID: uuid.NewString(), Action: "remove", SubtaskID: v.ID}); err != nil {
				return err
			}
		}
	}
	_, err = applySubtask(ctx, tx, p, a, jobID, SubtaskCommand{OperationID: uuid.NewString(), Action: "reorder", Order: order})
	return err
}
