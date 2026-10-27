package authorization

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5"
)

type JobAssetReference struct {
	ID, AssetID                                  int64
	Number, Name                                 string
	Operational, CanNavigate, Unlinked, Archived bool
}

// LinkedJobAssets exposes immutable link labels for historical references.
// It never follows a former relationship into current operational context.
func (s *Service) LinkedJobAssets(ctx context.Context, a Actor, id int64) ([]JobAssetReference, error) {
	var out []JobAssetReference
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, "job", id, "", false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT l.id,l.asset_id,l.number,l.name,l.operational AND l.unlinked_at IS NULL,l.unlinked_at IS NOT NULL,
   l.operational AND l.unlinked_at IS NULL AND a.deleted_at IS NULL AND a.customer_id=j.customer_id AND $2,a.deleted_at IS NOT NULL
   FROM job_asset_links l JOIN jobs j ON j.id=l.job_id JOIN assets a ON a.id=l.asset_id WHERE l.job_id=$1
   AND (NOT l.operational OR l.unlinked_at IS NOT NULL OR $2 OR ($3 AND j.deleted_at IS NULL AND EXISTS(SELECT 1 FROM job_assignments WHERE job_id=j.id AND user_id=$4)))
   ORDER BY l.id`, id, p.Allows("asset.view", All), p.Allows("asset.view", Context) && p.Allows("job.view", Assigned), p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v JobAssetReference
			if err = rows.Scan(&v.ID, &v.AssetID, &v.Number, &v.Name, &v.Operational, &v.Unlinked, &v.CanNavigate, &v.Archived); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

func (s *Service) ChangeJobAsset(ctx context.Context, a Actor, jobID, assetID, linkID int64, unlink bool) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" {
			return ErrForbidden
		}
		if _, err := parentAccess(ctx, tx, p, "job", jobID, "job.relations.edit", true); err != nil {
			return err
		}
		if err := saveaction.RequireRevision(ctx, tx, p.UserID, fmt.Sprintf("/jobs/%d", jobID)); err != nil {
			return err
		}
		if unlink {
			tag, err := tx.Exec(ctx, `UPDATE job_asset_links SET operational=false,unlinked_at=clock_timestamp() WHERE id=$1 AND job_id=$2 AND unlinked_at IS NULL`, linkID, jobID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			// Clear only the compatibility slot, never another explicit Asset link.
			if _, err = tx.Exec(ctx, `UPDATE jobs j SET asset_id=NULL WHERE j.id=$1 AND j.asset_id=(SELECT asset_id FROM job_asset_links WHERE id=$2)
 AND NOT EXISTS(SELECT 1 FROM job_asset_links l WHERE l.job_id=j.id AND l.asset_id=j.asset_id AND l.operational AND l.unlinked_at IS NULL)`, jobID, linkID); err != nil {
				return err
			}
		} else {
			if !p.Allows("asset.view", All) {
				return ErrNotFound
			}
			var valid bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets a JOIN jobs j ON j.customer_id=a.customer_id AND j.company_id=a.company_id JOIN customers c ON c.id=j.customer_id WHERE a.id=$1 AND j.id=$2 AND a.deleted_at IS NULL AND c.deleted_at IS NULL)`, assetID, jobID).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return ErrNotFound
			}
			var duplicate bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_asset_links WHERE job_id=$1 AND asset_id=$2 AND operational AND unlinked_at IS NULL)`, jobID, assetID).Scan(&duplicate); err != nil {
				return err
			}
			if duplicate {
				// Both exposed editors select the same canonical relationship. A
				// repeated selection must not replace its identity or provenance.
				return audit(ctx, tx, a, p, "job.relations.edit", All, "job", jobID, map[string]any{"asset_id": assetID, "existing_link_retained": true})
			}
			if err := tx.QueryRow(ctx, `INSERT INTO job_asset_links(job_id,asset_id,customer_id,number,name,linked_on,association_event_id,operational)
    SELECT $1,a.id,a.customer_id,a.number,a.name,(clock_timestamp() AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=$3),'UTC'))::date,coalesce((SELECT event_id FROM asset_association_facts WHERE asset_id=a.id ORDER BY effective_date DESC,event_id DESC LIMIT 1),0),true FROM assets a WHERE a.id=$2 RETURNING id`, jobID, assetID, p.CompanyID).Scan(&linkID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE jobs SET updated_at=clock_timestamp() WHERE id=$1`, jobID); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "job.relations.edit", All, "job", jobID, map[string]any{"asset_id": assetID, "link_id": linkID, "unlinked": unlink})
	})
}
