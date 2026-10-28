package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// LeaseLifecycle is an independent state-only command. It must not route
// through Manage/details validation or require new-selection authority.
func (s *Service) LeaseLifecycle(ctx context.Context, a Actor, id int64, restore bool) error {
	return s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) {
			return ErrNotFound
		}
		permission := "lease.archive"
		if restore {
			permission = "lease.restore"
		}
		if !p.Allows(permission, All) {
			return ErrForbidden
		}
		var customer int64
		var premises *int64
		var archived bool
		err := tx.QueryRow(ctx, `SELECT customer_id,premises_id,deleted_at IS NOT NULL FROM leases WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, p.CompanyID).Scan(&customer, &premises, &archived)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if archived && !p.Allows("lease.archived.view", All) {
			return ErrNotFound
		}
		if restore {
			active, err := customerLifecycleState(ctx, tx, p, customer, true)
			if err != nil {
				return err
			}
			if !active {
				return ErrConflict
			}
		}
		if archived == !restore {
			return nil
		}
		// SQL seam and native state share the current authority/master fence.
		// No intended Asset or retained premises is selected again or restored.
		if _, err := tx.Exec(ctx, `SELECT set_lease_master_dependencies($1,$2,$3,$4,'draft',$5,'[]'::jsonb)`, id, p.CompanyID, customer, premises, !restore); err != nil {
			return err
		}
		value := "clock_timestamp()"
		if restore {
			value = "NULL"
		}
		if _, err := tx.Exec(ctx, `UPDATE leases SET deleted_at=`+value+`,details_revision=details_revision+1,lifecycle_revision=lifecycle_revision+1,updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, permission, All, "lease", id, map[string]any{"before": map[string]bool{"archived": archived}, "after": map[string]bool{"archived": !restore}})
	})
}
