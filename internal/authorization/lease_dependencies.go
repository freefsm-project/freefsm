package authorization

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// LeaseDependencies is an explicit current dependency projection supplied by a
// Lease registration/finalization writer, never a reconstruction of its history.
type LeaseDependencies struct {
	LeaseID    int64
	CustomerID int64
	PremisesID *int64
	Lifecycle  string
	Archived   bool
	Coverage   []LeaseAssetDependency
}

type LeaseAssetDependency struct {
	AssetID  int64   `json:"asset_id"`
	StartsOn string  `json:"starts_on"`
	EndsOn   *string `json:"ends_on"`
}

// WriteLeaseDependencies must run in the SAME transaction as the Lease write.
// It acquires the shared master/authority fence before reading current grants,
// validates the entire replacement, and records an immutable before/after audit.
// The caller owns commit/rollback and the agreement's other lifecycle rules.
// It never infers history or changes placement or billing. Archive-state changes
// additionally require their independent lifecycle grant; manage implies neither.
func (s *Service) WriteLeaseDependencies(ctx context.Context, tx pgx.Tx, a Actor, in LeaseDependencies) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	if a.Channel == "api" || !p.Allows("lease.view", All) || !p.Allows("lease.manage", All) {
		return ErrForbidden
	}
	var before json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT lease_dependency_snapshot($1)`, in.LeaseID).Scan(&before); err != nil {
		return err
	}
	var prior struct {
		CompanyID  int64                  `json:"company_id"`
		CustomerID int64                  `json:"customer_id"`
		PremisesID *int64                 `json:"premises_id"`
		Archived   bool                   `json:"archived"`
		Coverage   []LeaseAssetDependency `json:"coverage"`
	}
	if err := json.Unmarshal(before, &prior); err != nil {
		return err
	}
	if prior.CompanyID != 0 && prior.CompanyID != p.CompanyID {
		return ErrNotFound
	}
	if prior.Archived && !p.Allows("lease.archived.view", All) {
		return ErrNotFound
	}
	if prior.Archived != in.Archived {
		permission := "lease.archive"
		if !in.Archived {
			permission = "lease.restore"
		}
		if !p.Allows(permission, All) {
			return ErrForbidden
		}
	}
	// Lease authority does not authorize selecting a known master ID. Retained
	// references may be kept/removed without expanding target visibility, while
	// changed selections require current All Records View and active same-tenant
	// targets. SQL still validates every retained dependency's domain eligibility.
	if in.CustomerID != prior.CustomerID {
		if !p.Allows("customer.view", All) {
			return ErrNotFound
		}
		active, err := customerLifecycleState(ctx, tx, p, in.CustomerID, true)
		if err != nil {
			return err
		}
		if !active {
			return ErrConflict
		}
	}
	if in.PremisesID != nil && (prior.PremisesID == nil || *in.PremisesID != *prior.PremisesID || in.CustomerID != prior.CustomerID) {
		if !p.Allows("location.view", All) {
			return ErrNotFound
		}
		var active bool
		err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM locations WHERE id=$1 AND company_id=$2 AND object_type='customer' AND object_id=$3 FOR UPDATE`, *in.PremisesID, p.CompanyID, in.CustomerID).Scan(&active)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !active {
			return ErrConflict
		}
	}
	type coverageKey struct {
		asset int64
		start string
	}
	retained := map[coverageKey]bool{}
	for _, row := range prior.Coverage {
		retained[coverageKey{row.AssetID, row.StartsOn}] = true
	}
	for _, row := range in.Coverage {
		if _, err := time.Parse(time.DateOnly, row.StartsOn); err != nil {
			return ErrConflict
		}
		if row.EndsOn != nil {
			if _, err := time.Parse(time.DateOnly, *row.EndsOn); err != nil {
				return ErrConflict
			}
		}
		// Moving the start/re-adding coverage is a new selected dependency, not a
		// retained interval. An end-date revision keeps its original selection.
		if retained[coverageKey{row.AssetID, row.StartsOn}] {
			continue
		}
		// Native Drafts save selection intent without reserving coverage. Opening
		// commencement coverage for that exact saved selection is not a new master
		// selection. Derive this from fenced persisted state, never caller flags;
		// later start changes/re-additions still require selection authority.
		if len(prior.Coverage) == 0 && row.EndsOn == nil && (in.Lifecycle == "scheduled" || in.Lifecycle == "active" || in.Lifecycle == "month_to_month") {
			var savedCommencement bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leases l JOIN lease_intended_assets i ON i.lease_id=l.id JOIN lease_master_dependencies d ON d.lease_id=l.id WHERE l.id=$1 AND l.company_id=$2 AND l.lifecycle='draft' AND d.lifecycle='draft' AND l.deleted_at IS NULL AND l.customer_id=$3 AND l.premises_id IS NOT DISTINCT FROM $4::bigint AND i.asset_id=$5 AND l.starts_on=$6::date)`, in.LeaseID, p.CompanyID, in.CustomerID, in.PremisesID, row.AssetID, row.StartsOn).Scan(&savedCommencement)
			if err != nil {
				return err
			}
			if savedCommencement {
				continue
			}
		}
		if !p.Allows("asset.view", All) {
			return ErrNotFound
		}
		var active bool
		err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM assets WHERE id=$1 AND company_id=$2 FOR UPDATE`, row.AssetID, p.CompanyID).Scan(&active)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !active {
			return ErrConflict
		}
	}
	if in.Coverage == nil {
		in.Coverage = []LeaseAssetDependency{}
	}
	coverage, err := json.Marshal(in.Coverage)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT set_lease_master_dependencies($1,$2,$3,$4,$5,$6,$7::jsonb)`, in.LeaseID, p.CompanyID, in.CustomerID, in.PremisesID, in.Lifecycle, in.Archived, coverage); err != nil {
		return err
	}
	var after json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT lease_dependency_snapshot($1)`, in.LeaseID).Scan(&after); err != nil {
		return err
	}
	// The fence holds grants stable; re-read identity/session after any row-lock
	// wait as well, including time-based expiry, before recording the effect.
	if _, err := principal(ctx, tx, a, false); err != nil {
		return err
	}
	return audit(ctx, tx, a, p, "lease.dependencies.change", All, "lease", in.LeaseID, map[string]any{"before": before, "after": after})
}
