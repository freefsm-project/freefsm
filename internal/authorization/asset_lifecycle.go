package authorization

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

func (s *Service) AssetLifecycle(ctx context.Context, a Actor, id int64, restore bool) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		action := "asset.archive"
		if restore {
			action = "asset.restore"
		}
		// Restore is separately authorized and requires archived visibility. The
		// generic mutable-parent gate intentionally rejects archived records.
		if _, err := parentAccess(ctx, tx, p, "asset", id, "", true); err != nil {
			return err
		}
		if !p.Allows(action, All) {
			return ErrForbidden
		}
		var before json.RawMessage
		var archived bool
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(a),deleted_at IS NOT NULL FROM assets a WHERE id=$1 FOR UPDATE`, id).Scan(&before, &archived); err != nil {
			return err
		}
		if archived == restore {
			if restore {
				if _, err := tx.Exec(ctx, `UPDATE assets SET deleted_at=NULL,updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
					return err
				}
			} else {
				var linked bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_asset_links l JOIN jobs j ON j.id=l.job_id LEFT JOIN statuses s ON s.id=j.status_id WHERE l.asset_id=$1 AND l.operational AND l.unlinked_at IS NULL AND j.deleted_at IS NULL AND coalesce(s.category_key,'') NOT IN ('job:completed','job:canceled')) OR EXISTS(SELECT 1 FROM assets WHERE id=$1 AND ownership_class='company_owned' AND customer_id IS NOT NULL)`, id).Scan(&linked); err != nil {
					return err
				}
				if linked {
					return ErrConflict
				}
				if _, err := tx.Exec(ctx, `UPDATE assets SET deleted_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
					return err
				}
			}
		} else {
			return ErrConflict
		}
		return audit(ctx, tx, a, p, action, All, "asset", id, map[string]any{"before": before})
	})
}
