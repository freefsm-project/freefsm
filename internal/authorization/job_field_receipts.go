package authorization

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// JobFieldExecutionReceipt exposes only recorded nonsensitive disposition. Fresh
// same-user authentication permits reconciliation, never client rebinding.
func (s *Service) JobFieldExecutionReceipt(ctx context.Context, a Actor, operation string) (JobExecutionReceipt, error) {
	var out JobExecutionReceipt
	if id, err := uuid.Parse(operation); err != nil || id == uuid.Nil {
		return out, ErrNotFound
	}
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, _ Principal) error {
		err := tx.QueryRow(ctx, `SELECT operation_id::text,outcome,created_at,coalesce(validation_reason,'') FROM job_field_operations WHERE operation_id=$1 AND user_id=$2`, operation, a.UserID).Scan(&out.OperationID, &out.Code, &out.AcceptedAt, &out.Reason)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return out, err
}
