package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// Joint review deliberately requires both independent facets. It is never used
// as a base/field projection or as a substitute for command-time validation.
type LeaseFinalizationReview struct {
	Base                DraftLeaseBase
	Billing             DraftLeaseBilling
	Finalized           bool
	ManagedBillingStart string
	Generated, Pending  int
	GenerationFailure   string
}

func (s *Service) ReviewLeaseFinalization(ctx context.Context, a Actor, id int64) (LeaseFinalizationReview, error) {
	var out LeaseFinalizationReview
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.view", All) || !p.Allows("lease.manage", All) || !p.Allows("lease.billing.view", All) || !p.Allows("lease.billing.manage", All) {
			return ErrForbidden
		}
		var err error
		out.Base, err = readLeaseBase(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if out.Base.Archived {
			return ErrForbidden
		}
		out.Billing, err = readLeaseBilling(ctx, tx, p, id)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT managed_billing_start::text FROM lease_finalizations WHERE lease_id=$1`, id).Scan(&out.ManagedBillingStart)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		out.Finalized = true
		if err := tx.QueryRow(ctx, `SELECT coalesce((SELECT failure FROM lease_billing_worker_state WHERE lease_id=$1),'')`, id).Scan(&out.GenerationFailure); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE g.invoice_id IS NOT NULL),count(*) FILTER(WHERE g.invoice_id IS NULL) FROM lease_billing_obligations o LEFT JOIN lease_invoice_generations g ON g.obligation_id=o.id WHERE o.lease_id=$1`, id).Scan(&out.Generated, &out.Pending)
	})
	return out, err
}
