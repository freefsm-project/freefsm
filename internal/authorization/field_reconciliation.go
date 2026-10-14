package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type FieldState struct {
	Values    json.RawMessage   `json:"values"`
	Revisions map[string]string `json:"revisions"`
	Receipt   *FieldReceipt     `json:"receipt,omitempty"`
}

type ReviewedFieldPatch struct {
	OperationID string            `json:"operation_id"`
	Revisions   map[string]string `json:"revisions"`
	Values      json.RawMessage   `json:"values"`
}

type jobFieldReviewKey struct{}

// WithJobFieldReview carries the originally displayed per-field tokens into the
// atomic planning transaction. It conveys no permission or trusted values.
func WithJobFieldReview(ctx context.Context, revisions map[string]string) context.Context {
	return context.WithValue(ctx, jobFieldReviewKey{}, revisions)
}

func requireJobFieldRevisions(ctx context.Context, tx pgx.Tx, company, id int64, old, input string) error {
	state, err := fieldState(ctx, tx, company, id, old)
	if err != nil {
		return err
	}
	var changed []customfields.Value
	if err := json.Unmarshal([]byte(input), &changed); err != nil {
		return err
	}
	revisions, _ := ctx.Value(jobFieldReviewKey{}).(map[string]string)
	for _, value := range changed {
		key := fmt.Sprint(value.DefinitionID)
		if revisions[key] == "" || revisions[key] != state.Revisions[key] {
			return &FieldConflict{"stale_field_revision"}
		}
	}
	return nil
}

type FieldConflict struct{ Code string }

func (e *FieldConflict) Error() string { return e.Code }

type FieldReceipt struct {
	OperationID string `json:"operation_id"`
	JobID       int64  `json:"job_id"`
	Outcome     string `json:"outcome"`
}

// Receipt lookup deliberately does not depend on current parent visibility and
// never returns stored intent or field values after access has been removed.
func (s *Service) FieldOperationReceipt(ctx context.Context, a Actor, id int64, key string) (FieldReceipt, error) {
	var receipt FieldReceipt
	if _, err := uuid.Parse(key); err != nil {
		return receipt, ErrNotFound
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		err := tx.QueryRow(ctx, `SELECT operation_id::text,job_id,outcome FROM job_field_operations WHERE user_id=$1 AND client_hash=$2 AND operation_id=$3 AND job_id=$4`, a.UserID, a.SessionHash, key, id).Scan(&receipt.OperationID, &receipt.JobID, &receipt.Outcome)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return receipt, err
}

func fieldState(ctx context.Context, tx pgx.Tx, company, id int64, values string) (FieldState, error) {
	state := FieldState{Values: json.RawMessage(values), Revisions: map[string]string{}}
	rows, err := tx.Query(ctx, `SELECT d.id,coalesce(r.revision::text,'00000000-0000-0000-0000-000000000000') FROM custom_field_definitions d LEFT JOIN job_field_revisions r ON r.definition_id=d.id AND r.job_id=$1 WHERE d.object_type='job' AND custom_field_applies_to_company(d.company_id,$2)`, id, company)
	if err != nil {
		return state, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var revision string
		if err = rows.Scan(&id, &revision); err != nil {
			return state, err
		}
		state.Revisions[fmt.Sprint(id)] = revision
	}
	return state, rows.Err()
}

func (s *Service) ReadFieldState(ctx context.Context, a Actor, id int64) (FieldState, error) {
	var state FieldState
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		values, _, err := fieldParent(ctx, tx, p, "job", id, "")
		if err != nil {
			return err
		}
		state, err = fieldState(ctx, tx, p.CompanyID, id, values)
		return err
	})
	return state, err
}

// Reviewed patches retain terminal disposition separately from mutable values.
// A retired field becoming active never retries a previously rejected intent.
func (s *Service) PatchFieldsReviewed(ctx context.Context, a Actor, id int64, in ReviewedFieldPatch) (FieldState, error) {
	return s.patchFieldsReviewed(ctx, a, id, in, customfields.ChangedValues)
}

// SaveFieldsReviewed is the online details boundary. It enforces all current
// required fields, while retaining the same per-field concurrency contract.
func (s *Service) SaveFieldsReviewed(ctx context.Context, a Actor, id int64, in ReviewedFieldPatch) (FieldState, error) {
	return s.patchFieldsReviewed(ctx, a, id, in, customfields.Details)
}

func (s *Service) patchFieldsReviewed(ctx context.Context, a Actor, id int64, in ReviewedFieldPatch, mode customfields.SaveMode) (FieldState, error) {
	var state FieldState
	if key, err := uuid.Parse(in.OperationID); err != nil || key == uuid.Nil {
		return state, &FieldConflict{"invalid_operation_id"}
	}
	intent, err := json.Marshal(in)
	if err != nil {
		return state, err
	}
	outcome := ""
	err = s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var same bool
		err := tx.QueryRow(ctx, `SELECT job_id=$4 AND intent=$5::jsonb,outcome FROM job_field_operations WHERE user_id=$1 AND client_hash=$2 AND operation_id=$3`, a.UserID, a.SessionHash, in.OperationID, id, intent).Scan(&same, &outcome)
		if err == nil {
			if !same {
				outcome = "operation_intent_changed"
			}
			// Receipt identity survives authority loss; comparison data does not.
			if old, _, readErr := fieldParent(ctx, tx, p, "job", id, ""); readErr == nil {
				state, err = fieldState(ctx, tx, p.CompanyID, id, old)
				if err != nil {
					return err
				}
			} else if !errors.Is(readErr, ErrNotFound) && !errors.Is(readErr, ErrForbidden) {
				return readErr
			}
			if outcome == "accepted" {
				return saveaction.ConfirmNoChange(ctx, tx)
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		persist := func() error {
			_, err := tx.Exec(ctx, `INSERT INTO job_field_operations(user_id,client_hash,operation_id,job_id,intent,outcome) VALUES($1,$2,$3,$4,$5,$6)`, a.UserID, a.SessionHash, in.OperationID, id, intent, outcome)
			return err
		}
		old, scope, err := fieldParent(ctx, tx, p, "job", id, "job.fields.edit", true)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden) {
			outcome = "not_found"
			if errors.Is(err, ErrForbidden) {
				outcome = "forbidden"
			}
			return persist()
		}
		if err != nil {
			return err
		}
		state, err = fieldState(ctx, tx, p.CompanyID, id, old)
		if err != nil {
			return err
		}
		var entered []customfields.Value
		if err = json.Unmarshal(in.Values, &entered); err != nil || entered == nil {
			outcome = "invalid_custom_field"
		} else {
			for _, v := range entered {
				key := fmt.Sprint(v.DefinitionID)
				if in.Revisions[key] == "" || in.Revisions[key] != state.Revisions[key] {
					outcome = "stale_field_revision"
					break
				}
			}
		}
		if outcome == "" {
			input := string(in.Values)
			next, err := customfields.Validate(ctx, tx, "job", old, &input, mode, p.CompanyID)
			var invalid *customfields.FieldError
			if errors.As(err, &invalid) {
				outcome = "invalid_custom_field"
			} else if err != nil {
				return err
			} else {
				var changed bool
				if err = tx.QueryRow(ctx, `SELECT $1::jsonb IS DISTINCT FROM $2::jsonb`, old, next).Scan(&changed); err != nil {
					return err
				}
				if changed {
					if _, err = tx.Exec(ctx, `UPDATE jobs SET custom_fields=$2::jsonb,updated_at=clock_timestamp() WHERE id=$1`, id, next); err != nil {
						return err
					}
					if err = audit(ctx, tx, a, p, "job.fields.edit", scope, "job", id, map[string]any{"before": old, "after": next, "operation_id": in.OperationID}); err != nil {
						return err
					}
				} else if err := saveaction.ConfirmNoChange(ctx, tx); err != nil {
					return err
				}
				state, err = fieldState(ctx, tx, p.CompanyID, id, next)
				if err != nil {
					return err
				}
				outcome = "accepted"
			}
		}
		return persist()
	})
	if err != nil {
		return FieldState{}, err
	}
	if outcome != "accepted" {
		saveaction.RejectBeforeWrite(ctx)
	}
	if outcome == "not_found" {
		return FieldState{}, ErrNotFound
	}
	if outcome == "forbidden" {
		return FieldState{}, ErrForbidden
	}
	// ID misuse is an error about a different attempted intent, not a new
	// disposition for the original immutable receipt. Lookup retains that fact.
	if outcome != "operation_intent_changed" {
		state.Receipt = &FieldReceipt{OperationID: in.OperationID, JobID: id, Outcome: outcome}
	}
	if outcome != "accepted" {
		return state, &FieldConflict{outcome}
	}
	return state, nil
}
