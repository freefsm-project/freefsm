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
		var matches int
		// The operation UUID is already recorded in an actual changed-value audit.
		// Never invent an effect for historical/no-op receipts without that fact.
		// Rejected hidden targets do not expose their resolved installed identity.
		err := tx.QueryRow(ctx, `SELECT operation_id::text,outcome,created_at,coalesce(validation_reason,''),
		 CASE WHEN outcome='accepted' THEN job_id ELSE 0 END,
		 CASE WHEN outcome='accepted' AND EXISTS(SELECT 1 FROM authorization_audit a
		 WHERE a.action='job.fields.edit' AND a.target_type='job' AND a.target_id=o.job_id
		 AND a.actor_id=o.user_id AND a.detail->>'operation_id'=o.operation_id::text)
		 THEN operation_id::text ELSE '' END,count(*) OVER ()
		 FROM job_field_operations o WHERE operation_id=$1 AND user_id=$2`, operation, a.UserID).Scan(&out.OperationID, &out.Code, &out.AcceptedAt, &out.Reason, &out.JobID, &out.EffectID, &matches)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err == nil && matches != 1 {
			out = JobExecutionReceipt{}
			return ErrConflict
		}
		return err
	})
	return out, err
}
