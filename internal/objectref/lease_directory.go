package objectref

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The native SQL Lease store participates in the existing closed directory.
type leaseDirectory struct {
	Directory
	pool *pgxpool.Pool
}

func NewLeaseDirectory(base Directory, pool *pgxpool.Pool) Directory {
	return &leaseDirectory{base, pool}
}

func (d *leaseDirectory) Exists(ctx context.Context, ref Ref, mode ExistenceMode) (bool, error) {
	if ref.Type != TypeLease {
		return d.Directory.Exists(ctx, ref, mode)
	}
	if err := validateExists(ref, mode); err != nil {
		return false, err
	}
	var exists bool
	err := d.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leases WHERE id=$1 AND ($2 OR deleted_at IS NULL))`, ref.ID, mode == ExistsAny).Scan(&exists)
	return exists, err
}

func (d *leaseDirectory) TargetCompanyID(ctx context.Context, ref Ref) (int64, error) {
	if ref.Type != TypeLease {
		return d.Directory.TargetCompanyID(ctx, ref)
	}
	var id int64
	err := d.pool.QueryRow(ctx, `SELECT company_id FROM leases WHERE id=$1`, ref.ID).Scan(&id)
	return id, err
}

func (d *leaseDirectory) DisplayName(ctx context.Context, ref Ref) (string, error) {
	if ref.Type != TypeLease {
		return d.Directory.DisplayName(ctx, ref)
	}
	var name string
	err := d.pool.QueryRow(ctx, `SELECT number_label FROM leases WHERE id=$1`, ref.ID).Scan(&name)
	return name, err
}
