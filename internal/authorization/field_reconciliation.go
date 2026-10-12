package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type FieldState struct {
	Values    json.RawMessage   `json:"values"`
	Revisions map[string]string `json:"revisions"`
}

type ReviewedFieldPatch struct {
	OperationID string            `json:"operation_id"`
	Revisions   map[string]string `json:"revisions"`
	Values      json.RawMessage   `json:"values"`
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

func fieldState(ctx context.Context, tx pgx.Tx, id int64, values string) (FieldState, error) {
	state := FieldState{Values: json.RawMessage(values), Revisions: map[string]string{}}
	rows, err := tx.Query(ctx, `SELECT d.id,coalesce(r.revision::text,'00000000-0000-0000-0000-000000000000') FROM custom_field_definitions d LEFT JOIN job_field_revisions r ON r.definition_id=d.id AND r.job_id=$1 WHERE d.object_type='job'`, id)
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
		state, err = fieldState(ctx, tx, id, values)
		return err
	})
	return state, err
}

// Reviewed patches retain terminal disposition separately from mutable values.
// A retired field becoming active never retries a previously rejected intent.
func (s *Service) PatchFieldsReviewed(ctx context.Context, a Actor, id int64, in ReviewedFieldPatch) (FieldState, error) {
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
		old, scope, err := fieldParent(ctx, tx, p, "job", id, "job.fields.edit", true)
		if err != nil {
			return err
		}
		state, err = fieldState(ctx, tx, id, old)
		if err != nil {
			return err
		}
		var same bool
		err = tx.QueryRow(ctx, `SELECT job_id=$4 AND intent=$5::jsonb,outcome FROM job_field_operations WHERE user_id=$1 AND client_hash=$2 AND operation_id=$3`, a.UserID, a.SessionHash, in.OperationID, id, intent).Scan(&same, &outcome)
		if err == nil {
			if !same {
				outcome = "operation_intent_changed"
			}
			return nil
		}
		if err != pgx.ErrNoRows {
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
			next, err := customfields.Validate(ctx, tx, "job", old, &input, customfields.ChangedValues, p.CompanyID)
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
				}
				state, err = fieldState(ctx, tx, id, next)
				if err != nil {
					return err
				}
				outcome = "accepted"
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO job_field_operations(user_id,client_hash,operation_id,job_id,intent,outcome) VALUES($1,$2,$3,$4,$5,$6)`, a.UserID, a.SessionHash, in.OperationID, id, intent, outcome)
		return err
	})
	if err != nil {
		return FieldState{}, err
	}
	if outcome != "accepted" {
		return state, &FieldConflict{outcome}
	}
	return state, nil
}
