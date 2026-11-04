package authorization

import (
	"context"
	"slices"
	"time"

	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/jackc/pgx/v5"
)

// DraftLeaseBase contains no financial fields, even for a fully privileged actor.
type DraftLeaseBase struct {
	LeaseCalendar
	// Coverage is the retained finalized timeline, not Draft selection intent.
	Coverage                                                  []LeaseAssetDependency
	UnplacedAssetIDs, WrongPremisesAssetIDs                   []int64
	ID, CustomerID, Revision                                  int64
	Number, Title, Reference, ExecutionDate, StartsOn, EndsOn string
	PremisesID, AgreementFileID                               *int64
	AssetIDs                                                  []int64
	CustomFields                                              string
	CanManage                                                 bool
	CanReviewFinalization                                     bool
	Archived, CanArchive, CanRestore, PremisesArchived        bool
	ArchivedAssetIDs                                          []int64
}

type DraftLeaseDetailsInput struct {
	Revision                                          int64
	Title, Reference, ExecutionDate, StartsOn, EndsOn string
	PremisesID                                        *int64
	AssetIDs                                          []int64
	// Nil patch preserves the agreement selection when File authority is absent.
	AgreementFileID **int64
	Fields          *string
}

func readLeaseBase(ctx context.Context, tx pgx.Tx, p Principal, id int64) (DraftLeaseBase, error) {
	var out DraftLeaseBase
	err := tx.QueryRow(ctx, `SELECT id,customer_id,details_revision,number_label,title,reference,coalesce(execution_date::text,''),coalesce(starts_on::text,''),coalesce(ends_on::text,''),premises_id,agreement_file_id,custom_fields::text,ARRAY(SELECT asset_id FROM lease_intended_assets WHERE lease_id=l.id ORDER BY asset_id),deleted_at IS NOT NULL,EXISTS(SELECT 1 FROM locations WHERE id=l.premises_id AND deleted_at IS NOT NULL),ARRAY(SELECT i.asset_id FROM lease_intended_assets i JOIN assets a ON a.id=i.asset_id WHERE i.lease_id=l.id AND a.deleted_at IS NOT NULL ORDER BY i.asset_id) FROM leases l WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, p.CompanyID).Scan(&out.ID, &out.CustomerID, &out.Revision, &out.Number, &out.Title, &out.Reference, &out.ExecutionDate, &out.StartsOn, &out.EndsOn, &out.PremisesID, &out.AgreementFileID, &out.CustomFields, &out.AssetIDs, &out.Archived, &out.PremisesArchived, &out.ArchivedAssetIDs)
	if err == pgx.ErrNoRows {
		err = ErrNotFound
	}
	if err == nil && out.Archived && !p.Allows("lease.archived.view", All) {
		err = ErrNotFound
	}
	if err == nil {
		out.LeaseCalendar, err = readLeaseCalendar(ctx, tx, id)
	}
	if err == nil {
		var rows pgx.Rows
		rows, err = tx.Query(ctx, `SELECT asset_id,starts_on::text,ends_on::text FROM lease_asset_dependencies WHERE lease_id=$1 ORDER BY asset_id,starts_on`, id)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var coverage LeaseAssetDependency
			if err := rows.Scan(&coverage.AssetID, &coverage.StartsOn, &coverage.EndsOn); err != nil {
				return out, err
			}
			out.Coverage = append(out.Coverage, coverage)
		}
		err = rows.Err()
	}
	if err == nil {
		err = tx.QueryRow(ctx, `WITH selected AS (SELECT i.asset_id FROM lease_intended_assets i JOIN leases l ON l.id=i.lease_id WHERE i.lease_id=$1 AND l.lifecycle='draft' UNION SELECT d.asset_id FROM lease_asset_dependencies d JOIN leases l ON l.id=d.lease_id WHERE d.lease_id=$1 AND (d.ends_on IS NULL OR d.ends_on>=lease_dependency_today(l.company_id))) SELECT ARRAY(SELECT a.id FROM selected i JOIN assets a ON a.id=i.asset_id WHERE a.customer_id IS NULL ORDER BY a.id),ARRAY(SELECT a.id FROM selected i JOIN assets a ON a.id=i.asset_id JOIN leases l ON l.id=$1 WHERE a.customer_id=l.customer_id AND a.location_id IS DISTINCT FROM l.premises_id ORDER BY a.id)`, id).Scan(&out.UnplacedAssetIDs, &out.WrongPremisesAssetIDs)
	}
	return out, err
}

func (s *Service) DraftLeaseBase(ctx context.Context, a Actor, id int64) (DraftLeaseBase, error) {
	var out DraftLeaseBase
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) {
			return ErrNotFound
		}
		var err error
		out, err = readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		out.CanManage = !out.Archived && out.Lifecycle == "draft" && p.Allows("lease.manage", All)
		out.CanReviewFinalization = !out.Archived && p.Allows("lease.manage", All) && p.Allows("lease.billing.view", All) && p.Allows("lease.billing.manage", All)
		out.CanArchive = !out.Archived && (out.Lifecycle == "draft" || out.Lifecycle == "terminated") && p.Allows("lease.archive", All)
		if out.Archived && p.Allows("lease.restore", All) {
			if out.Lifecycle == "terminated" {
				// Restoring retained history has no operational Customer prerequisite.
				out.CanRestore = true
			} else if out.Lifecycle == "draft" {
				out.CanRestore, err = customerLifecycleState(ctx, tx, p, out.CustomerID, true)
				if err != nil {
					return err
				}
			}
		}
		if !p.Allows("file.download", All) {
			out.AgreementFileID = nil
		} else if out.AgreementFileID != nil {
			var visible bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM files WHERE id=$1 AND company_id=$2 AND object_type='lease' AND object_id=$3 AND removed_at IS NULL)`, *out.AgreementFileID, p.CompanyID, id).Scan(&visible); err != nil {
				return err
			}
			if !visible {
				out.AgreementFileID = nil
			}
		}
		return nil
	})
	return out, err
}

func (s *Service) SaveDraftLeaseDetails(ctx context.Context, a Actor, id int64, in DraftLeaseDetailsInput) error {
	return s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		// Validation and selection checks are read-only. The dependency writer
		// is the first mutating call; errors after dispatch remain conservative.
		writeDispatched := false
		defer func() {
			if !writeDispatched {
				saveaction.RejectBeforeWrite(ctx)
			}
		}()
		if a.Channel == "api" || !p.Allows("lease.view", All) || !p.Allows("lease.manage", All) {
			return ErrForbidden
		}
		old, err := readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if old.Revision != in.Revision {
			return ErrConflict
		}
		if old.Archived {
			return ErrForbidden
		}
		if old.Lifecycle != "draft" {
			return ErrConflict
		}
		for _, date := range []string{in.ExecutionDate, in.StartsOn, in.EndsOn} {
			if date != "" {
				if _, err := time.Parse(time.DateOnly, date); err != nil {
					return ErrConflict
				}
			}
		}
		fields, err := customfields.Validate(ctx, tx, "lease", old.CustomFields, in.Fields, customfields.Details, p.CompanyID)
		if err != nil {
			return err
		}
		// Retained stale intended Assets are history, not a new active selection.
		for _, asset := range in.AssetIDs {
			if slices.Contains(old.AssetIDs, asset) {
				continue
			}
			if !p.Allows("asset.view", All) {
				return ErrNotFound
			}
			var valid bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assets WHERE id=$1 AND company_id=$2 AND ownership_class='company_owned' AND deleted_at IS NULL)`, asset, p.CompanyID).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return ErrConflict
			}
		}
		agreement := old.AgreementFileID
		if in.AgreementFileID != nil {
			if !p.Allows("file.download", All) {
				return ErrForbidden
			}
			agreement = *in.AgreementFileID
			if agreement != nil {
				var valid bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM files WHERE id=$1 AND company_id=$2 AND object_type='lease' AND object_id=$3 AND removed_at IS NULL)`, *agreement, p.CompanyID, id).Scan(&valid); err != nil {
					return err
				}
				if !valid {
					return ErrNotFound
				}
				if _, err := fileAccess(ctx, tx, p, a, objectref.New(objectref.TypeLease, id), "file.download", 0, true); err != nil {
					return err
				}
			}
		}
		writeDispatched = true
		if err := s.WriteLeaseDependencies(ctx, tx, a, LeaseDependencies{LeaseID: id, CustomerID: old.CustomerID, PremisesID: in.PremisesID, Lifecycle: "draft"}); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE leases SET title=$2,reference=$3,execution_date=NULLIF($4,'')::date,starts_on=NULLIF($5,'')::date,ends_on=NULLIF($6,'')::date,premises_id=$7,agreement_file_id=$8,custom_fields=$9::jsonb,details_revision=details_revision+1,updated_at=clock_timestamp() WHERE id=$1`, id, in.Title, in.Reference, in.ExecutionDate, in.StartsOn, in.EndsOn, in.PremisesID, agreement, fields)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM lease_intended_assets WHERE lease_id=$1 AND NOT(asset_id=ANY($2::bigint[]))`, id, in.AssetIDs); err != nil {
			return err
		}
		for _, asset := range in.AssetIDs {
			if !slices.Contains(old.AssetIDs, asset) {
				if _, err = tx.Exec(ctx, `INSERT INTO lease_intended_assets VALUES($1,$2) ON CONFLICT DO NOTHING`, id, asset); err != nil {
					return err
				}
			}
		}
		after, err := readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "lease.details.change", All, "lease", id, map[string]any{"before": old, "after": after})
	})
}
