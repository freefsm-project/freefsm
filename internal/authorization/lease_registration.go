package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// LeaseRegistration is the minimal base projection, deliberately independent of
// billing and Asset coverage. Registration records an externally agreed Lease.
type LeaseRegistration struct {
	ID         int64
	CustomerID int64
	Number     string
}

func (s *Service) RegisterDraftLease(ctx context.Context, a Actor, customerID int64) (LeaseRegistration, error) {
	var out LeaseRegistration
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) || !p.Allows("lease.manage", All) {
			return ErrForbidden
		}
		if !p.Allows("customer.view", All) {
			return ErrNotFound
		}
		active, err := customerLifecycleState(ctx, tx, p, customerID, true)
		if err != nil {
			return err
		}
		if !active {
			return ErrConflict
		}
		if err := tx.QueryRow(ctx, `INSERT INTO leases(company_id,customer_id) VALUES($1,$2) RETURNING id,customer_id,number_label`, p.CompanyID, customerID).Scan(&out.ID, &out.CustomerID, &out.Number); err != nil {
			return err
		}
		if err := s.WriteLeaseDependencies(ctx, tx, a, LeaseDependencies{LeaseID: out.ID, CustomerID: customerID, Lifecycle: "draft"}); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "lease.create", All, "lease", out.ID, out)
	})
	if err != nil {
		return LeaseRegistration{}, err
	}
	return out, nil
}

func (s *Service) DraftLeaseRegistration(ctx context.Context, a Actor, id int64) (LeaseRegistration, error) {
	var out LeaseRegistration
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) {
			return ErrNotFound
		}
		err := tx.QueryRow(ctx, `SELECT id,customer_id,number_label FROM leases WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL`, id, p.CompanyID).Scan(&out.ID, &out.CustomerID, &out.Number)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	})
	return out, err
}
