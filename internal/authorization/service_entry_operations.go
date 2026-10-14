package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ServiceEntryCommand struct {
	OccurredAt  *time.Time        `json:"occurred_at,omitempty"`
	OperationID string            `json:"operation_id"`
	JobID       int64             `json:"job_id"`
	AssetID     int64             `json:"asset_id"`
	EntryID     int64             `json:"entry_id"`
	Revision    string            `json:"revision"`
	Content     ServiceEntryInput `json:"content"`
}
type ServiceEntryReceipt struct {
	OperationID string    `json:"operation_id"`
	EntryID     int64     `json:"entry_id,omitempty"`
	Code        string    `json:"code"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

func serviceEntryOutcome(code string) error {
	switch code {
	case "not_found":
		return ErrNotFound
	case "forbidden":
		return ErrForbidden
	case "service_entry_conflict":
		return ErrConflict
	}
	return nil
}

// SubmitServiceEntry commits a domain effect, audit and immutable domain receipt
// together, admitted by the shared cross-family operation registry.
func (s *Service) SubmitServiceEntry(ctx context.Context, a Actor, in ServiceEntryCommand) (ServiceEntryReceipt, error) {
	result := ServiceEntryReceipt{OperationID: in.OperationID}
	op, err := uuid.Parse(in.OperationID)
	if err != nil || op == uuid.Nil {
		return result, ErrConflict
	}
	in.OperationID = op.String()
	result.OperationID = in.OperationID
	raw, err := json.Marshal(in)
	if err != nil {
		return result, err
	}
	var outcome error
	err = s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		kind := "service_entry.create"
		if in.EntryID != 0 {
			kind = "service_entry.revise"
		}
		if err := AdmitOperation(ctx, tx, a, in.OperationID, kind, in); err != nil {
			return err
		}
		var same bool
		err := tx.QueryRow(ctx, `SELECT user_id=$2 AND client_key=$3 AND intent=$4::jsonb,coalesce(entry_id,0),code,accepted_at FROM service_entry_operation_receipts WHERE operation_id=$1`, in.OperationID, a.UserID, operationClientKey(a), raw).Scan(&same, &result.EntryID, &result.Code, &result.AcceptedAt)
		if err == nil {
			if !same {
				return ErrConflict
			}
			outcome = serviceEntryOutcome(result.Code)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Savepoint makes even future multi-step validation failures non-partial.
		effect, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		result.EntryID, outcome = applyServiceEntry(ctx, effect, p, a, in.JobID, in.AssetID, in.EntryID, in.Revision, in.Content, in.OperationID, in.OccurredAt)
		if outcome == nil {
			if err = effect.Commit(ctx); err != nil {
				return err
			}
			result.Code = "accepted"
		} else {
			if err = effect.Rollback(ctx); err != nil {
				return err
			}
			result.EntryID = 0
			switch {
			case errors.Is(outcome, ErrNotFound):
				result.Code = "not_found"
			case errors.Is(outcome, ErrForbidden):
				result.Code = "forbidden"
			case errors.Is(outcome, ErrConflict):
				result.Code = "service_entry_conflict"
			default:
				return outcome
			}
		}
		return tx.QueryRow(ctx, `INSERT INTO service_entry_operation_receipts(operation_id,user_id,client_key,intent,entry_id,code) VALUES($1,$2,$3,$4,nullif($5,0),$6) RETURNING accepted_at`, in.OperationID, a.UserID, operationClientKey(a), raw, result.EntryID, result.Code).Scan(&result.AcceptedAt)
	})
	if err != nil {
		return ServiceEntryReceipt{}, err
	}
	return result, outcome
}

func (s *Service) ServiceEntryReceipt(ctx context.Context, a Actor, operation string) (ServiceEntryReceipt, error) {
	result := ServiceEntryReceipt{OperationID: operation}
	if _, err := uuid.Parse(operation); err != nil {
		return result, ErrNotFound
	}
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		err := tx.QueryRow(ctx, `SELECT coalesce(entry_id,0),code,accepted_at FROM service_entry_operation_receipts WHERE operation_id=$1 AND user_id=$2 AND client_key=$3`, operation, a.UserID, operationClientKey(a)).Scan(&result.EntryID, &result.Code, &result.AcceptedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return result, err
}
