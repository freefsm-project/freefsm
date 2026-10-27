package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// JobLifecycle closes or restores the Job itself. Documents, time entries and
// historical master links retain their independent lifecycles.
func (s *Service) JobLifecycle(ctx context.Context, a Actor, id int64, restore bool) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, "job", id, "", true); err != nil {
			return err
		}
		verb := "archive"
		if restore {
			verb = "restore"
		}
		if !p.Allows("job."+verb, All) {
			return ErrForbidden
		}
		var customerID int64
		var archived bool
		var category string
		if err := tx.QueryRow(ctx, `SELECT j.customer_id,j.deleted_at IS NOT NULL,coalesce(s.category_key,'') FROM jobs j LEFT JOIN statuses s ON s.id=j.status_id WHERE j.id=$1`, id).Scan(&customerID, &archived, &category); err != nil {
			return err
		}
		if restore {
			active, err := customerLifecycleState(ctx, tx, p, customerID, true)
			if err != nil {
				return err
			}
			if !active {
				return ErrConflict
			}
		} else if category != "job:completed" && category != "job:canceled" {
			return ErrConflict
		}
		if !restore {
			// Effective status matters: a zero-total Draft is still Draft. A
			// converted source is hidden, but its independent Invoice is checked.
			var blocked bool
			if err := tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM invoices i LEFT JOIN invoice_effective_status e ON e.invoice_id=i.id LEFT JOIN statuses s ON s.id=e.status_id
 WHERE i.job_id=$1 AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND coalesce(s.category_key,'') NOT IN ('invoice:paid','invoice:void'))
 OR EXISTS(SELECT 1 FROM estimates e LEFT JOIN statuses s ON s.id=e.status_id
 WHERE e.job_id=$1 AND e.deleted_at IS NULL AND e.conversion_hidden_at IS NULL AND coalesce(s.category_key,'') NOT IN ('estimate:rejected','estimate:completed'))`, id).Scan(&blocked); err != nil {
				return err
			}
			if blocked {
				return ErrConflict
			}
		}
		if archived == !restore {
			return nil
		}
		before, err := lifecycleSnapshot(ctx, tx, p, "job", id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET deleted_at=CASE WHEN $2 THEN NULL ELSE clock_timestamp() END,updated_at=clock_timestamp() WHERE id=$1`, id, restore); err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, a, p, "job", id, verb, before, map[string]any{"category": category})
	})
}
