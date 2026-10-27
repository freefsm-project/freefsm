package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// CommercialRestoreAccess is advisory discovery. Restore rechecks the same
// authority and current Customer under the shared committing fence. A retained
// archived Job is historical provenance, not a restore dependency.
func (s *Service) CommercialRestoreAccess(ctx context.Context, a Actor, kind string, id int64) error {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return err
	}
	return commercialRestoreAccess(ctx, s.Pool, p, kind, id, false)
}

func commercialRestoreAccess(ctx context.Context, q queryer, p Principal, kind string, id int64, locking bool) error {
	if kind != "estimate" && kind != "invoice" {
		return ErrNotFound
	}
	if _, err := parentAccess(ctx, q, p, kind, id, "", locking); err != nil {
		return err
	}
	if !p.Allows(kind+".restore", All) || !p.Allows(kind+".archived.view", All) {
		return ErrForbidden
	}
	var eligible bool
	lock := ""
	if locking {
		lock = " FOR SHARE OF c"
	}
	err := q.QueryRow(ctx, `SELECT d.deleted_at IS NOT NULL AND c.deleted_at IS NULL
	 FROM `+kind+`s d JOIN customers c ON c.id=d.customer_id AND c.company_id=d.company_id
	 WHERE d.id=$1 AND d.company_id=$2`+lock, id, p.CompanyID).Scan(&eligible)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !eligible {
		return ErrConflict
	}
	return nil
}

func (s *Service) RestoreCommercialDocument(ctx context.Context, a Actor, kind string, id int64) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if err := commercialRestoreAccess(ctx, tx, p, kind, id, true); err != nil {
			return err
		}
		before, err := lifecycleSnapshot(ctx, tx, p, kind, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE `+kind+`s SET deleted_at=NULL,updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, a, p, kind, id, "restore", before, nil)
	})
}

func (s *Service) ArchiveCommercialDocument(ctx context.Context, a Actor, kind string, id int64) error {
	if kind != "estimate" && kind != "invoice" {
		return ErrNotFound
	}
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, kind, id, kind+".archive", true); err != nil {
			return err
		}
		if !p.Allows(kind+".archive", All) {
			return ErrForbidden
		}
		if kind == "invoice" {
			var eligible bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoice_effective_status e JOIN statuses s ON s.id=e.status_id WHERE e.invoice_id=$1 AND s.category_key IN ('invoice:paid','invoice:void'))`, id).Scan(&eligible); err != nil {
				return err
			}
			if !eligible {
				return ErrConflict
			}
		}
		before, err := lifecycleSnapshot(ctx, tx, p, kind, id)
		if err != nil {
			return err
		}
		// Existing database triggers cancel all pre-handoff Delivery attempts and
		// their retries atomically, under the same dispatch/settlement fence.
		if _, err := tx.Exec(ctx, `UPDATE `+kind+`s SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
		return auditLifecycle(ctx, tx, a, p, kind, id, "archive", before, nil)
	})
}
