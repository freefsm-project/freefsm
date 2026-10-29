package authorization

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// RetryLeaseBillingPeriod uses the reviewed durable obligation, never creates a
// fresh intent. Failed commercial effects roll back while the retry disposition
// remains visible. Successful lost-response replay returns its immutable receipt.
func (s *Service) RetryLeaseBillingPeriod(ctx context.Context, a Actor, lease, obligation int64, review, key string) (int64, error) {
	var invoice int64
	var generationErr error
	err := s.WithinActor(ctx, a, func(tx pgx.Tx, p Principal) error {
		if a.Channel == "api" || !p.Allows("lease.billing.view", All) || !p.Allows("lease.billing.manage", All) || !p.Allows("lease.manage", All) {
			return ErrForbidden
		}
		var owner, period int64
		var saved string
		err := tx.QueryRow(ctx, `SELECT r.invoice_id,r.actor_id,r.obligation_id,r.review_hash FROM lease_billing_retry_receipts r JOIN lease_billing_obligations o ON o.id=r.obligation_id JOIN leases l ON l.id=o.lease_id WHERE r.operation_key=$1 AND l.id=$2 AND l.company_id=$3`, key, lease, p.CompanyID).Scan(&invoice, &owner, &period, &saved)
		if err == nil {
			if owner != p.UserID || period != obligation || saved != review {
				return ErrConflict
			}
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}
		current, err := readLeaseBillingPeriod(ctx, tx, p, lease, obligation)
		if err != nil {
			return err
		}
		if !current.CanRetry || current.ReviewHash != review || current.OperationKey != key {
			return ErrConflict
		}
		work, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		document, attemptErr := approvedLeasePeriod(ctx, work, p, lease, obligation)
		if attemptErr == nil {
			invoice, attemptErr = s.createBoundDocument(ctx, work, a, p, "invoice", document, func(string) (string, error) { return "unpaid", nil }, obligation)
		}
		if attemptErr == nil {
			attemptErr = recordLeaseGeneration(ctx, work, lease, obligation, invoice)
		}
		if attemptErr == nil {
			_, attemptErr = work.Exec(ctx, `INSERT INTO lease_billing_retry_receipts(operation_key,obligation_id,invoice_id,actor_id,review_hash) VALUES($1,$2,$3,$4,$5)`, key, obligation, invoice, p.UserID, review)
		}
		if attemptErr == nil {
			attemptErr = audit(ctx, work, a, p, "lease.billing.retry", All, "lease", lease, map[string]any{"obligation_id": obligation, "invoice_id": invoice, "operation_key": key})
		}
		if attemptErr == nil {
			_, attemptErr = work.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
		}
		if attemptErr != nil {
			if err := work.Rollback(ctx); err != nil {
				return err
			}
			generationErr = attemptErr
			if _, err := tx.Exec(ctx, `INSERT INTO lease_billing_attempts(obligation_id,failure) VALUES($1,'Draft generation failed; review current agreement, coverage and billing terms, then retry.') ON CONFLICT(obligation_id) DO UPDATE SET attempted_at=clock_timestamp(),failure=excluded.failure`, obligation); err != nil {
				return err
			}
			return audit(ctx, tx, a, p, "lease.billing.retry_failed", All, "lease", lease, map[string]any{"obligation_id": obligation, "operation_key": key})
		}
		return work.Commit(ctx)
	})
	if err != nil {
		return 0, err
	}
	if generationErr != nil {
		return 0, generationErr
	}
	return invoice, nil
}
