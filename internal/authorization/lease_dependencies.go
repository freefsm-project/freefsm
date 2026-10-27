package authorization

import (
	"context"
	"encoding/json"

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
	if a.Channel == "api" || !p.Allows("lease.manage", All) {
		return ErrForbidden
	}
	var before json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT lease_dependency_snapshot($1)`, in.LeaseID).Scan(&before); err != nil {
		return err
	}
	var prior struct {
		Archived bool `json:"archived"`
	}
	if err := json.Unmarshal(before, &prior); err != nil {
		return err
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
	return audit(ctx, tx, a, p, "lease.dependencies.change", All, "lease", in.LeaseID, map[string]any{"before": before, "after": after})
}
