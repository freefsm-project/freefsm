package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type DeliveryReceipt struct {
	OperationID string  `json:"operation_id"`
	Outcome     string  `json:"outcome"`
	DeliveryIDs []int64 `json:"delivery_ids,omitempty"`
}

// DeliveryTransaction supplies a current authenticated principal under the same
// commit fence used by grants, sessions, PDF and File mutations.
func (s *Service) DeliveryTransaction(ctx context.Context, a Actor, fn func(pgx.Tx, Principal) error) error {
	return s.transaction(ctx, a, false, fn)
}

func deliveryReceipt(ctx context.Context, tx pgx.Tx, a Actor, id string) (DeliveryReceipt, error) {
	var r DeliveryReceipt
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT receipt FROM delivery_operations WHERE operation_id=$1 AND user_id=$2 AND client_key=$3`, id, a.UserID, operationClientKey(a)).Scan(&raw)
	if err == pgx.ErrNoRows {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(raw, &r)
	return r, err
}

func (s *Service) DeliveryOperationReceipt(ctx context.Context, a Actor, id string) (DeliveryReceipt, error) {
	var r DeliveryReceipt
	if key, err := uuid.Parse(id); err != nil || key == uuid.Nil {
		return r, ErrNotFound
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, _ Principal) error {
		var err error
		r, err = deliveryReceipt(ctx, tx, a, id)
		return err
	})
	return r, err
}

func (s *Service) AcceptDeliveryOperation(ctx context.Context, a Actor, id, kind string, document int64, intent any, occurred *time.Time, fn func(pgx.Tx, Principal) ([]int64, error)) (DeliveryReceipt, error) {
	r := DeliveryReceipt{OperationID: id}
	if key, err := uuid.Parse(id); err != nil || key == uuid.Nil || key.String() != id || a.Channel != "api" || occurred != nil && (occurred.IsZero() || occurred.After(time.Now())) {
		return r, ErrCommercialInput
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := admitOperation(ctx, tx, a, id, kind+".delivery.request", map[string]any{"document_id": document, "intent": intent}); err != nil {
			return err
		}
		prior, err := deliveryReceipt(ctx, tx, a, id)
		if err == nil {
			r = prior
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		ids, effect := fn(sp, p)
		var invalid *FileValidationError
		if effect != nil {
			_ = sp.Rollback(ctx)
			switch {
			case errors.Is(effect, ErrConflict):
				r.Outcome = "source_drift"
			case errors.Is(effect, ErrForbidden):
				r.Outcome = "forbidden"
			case errors.Is(effect, ErrNotFound):
				r.Outcome = "not_found"
			case errors.As(effect, &invalid):
				r.Outcome = "invalid_attachments"
			default:
				return effect
			}
		} else {
			scope, err := pdfDocumentAccess(ctx, sp, p, kind, document, "delivery.request")
			if err != nil {
				return err
			}
			detail := map[string]any{"operation_id": id, "delivery_ids": ids}
			if occurred != nil {
				detail["occurred_at"] = occurred.UTC()
			}
			if err := audit(ctx, sp, a, p, kind+".delivery.request", scope, kind, document, detail); err != nil {
				return err
			}
			if err := sp.Commit(ctx); err != nil {
				return err
			}
			r.Outcome = "accepted"
			r.DeliveryIDs = ids
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO delivery_operations(operation_id,user_id,client_key,receipt) VALUES($1,$2,$3,$4)`, id, a.UserID, operationClientKey(a), raw)
		return err
	})
	return r, err
}
