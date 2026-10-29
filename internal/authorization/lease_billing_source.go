package authorization

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// RecordLeaseBillingSourceChange is the transaction-bound review seam for owning
// agreement/term/cutoff/charge writers. The caller owns the approved source
// revision and its invariants; this hook is not an amendment or repricing API.
// Period bounds are inclusive coverage starts, not Invoice creation dates.
func (s *Service) RecordLeaseBillingSourceChange(ctx context.Context, tx pgx.Tx, a Actor, lease int64, first string, last *string, reason string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	if a.Channel == "api" || !p.Allows("lease.manage", All) || !p.Allows("lease.billing.manage", All) {
		return ErrForbidden
	}
	from, err := time.Parse(time.DateOnly, first)
	if err != nil || strings.TrimSpace(reason) == "" {
		return ErrConflict
	}
	if last != nil {
		to, err := time.Parse(time.DateOnly, *last)
		if err != nil || to.Before(from) {
			return ErrConflict
		}
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM leases l JOIN lease_finalizations f ON f.lease_id=l.id WHERE l.id=$1 AND l.company_id=$2)`, lease, p.CompanyID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	var change int64
	if err = tx.QueryRow(ctx, `SELECT record_lease_billing_source_change($1,$2::date,$3::date,$4)`, lease, first, last, reason).Scan(&change); err != nil {
		return err
	}
	return audit(ctx, tx, a, p, "lease.billing.source_change", All, "lease", lease, map[string]any{"change_id": change, "starts_on": first, "ends_on": last, "reason": reason})
}
