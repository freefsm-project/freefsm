package delivery

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// beginDispatch is the explicit no-recall boundary between Claim and invoking
// the transport. It records dispatch admission, never SMTP acceptance. Archive
// serializes with this decision; a claimed but not admitted attempt can cancel.
// Network I/O never holds the authorization mutex or document/Customer locks.
func (s *Service) beginDispatch(ctx context.Context, d Delivery) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return false, err
	}
	var active bool
	q := fmt.Sprintf(`SELECT o.deleted_at IS NULL AND o.conversion_hidden_at IS NULL AND c.deleted_at IS NULL AND ($3<>'invoice' OR coalesce(s.category_key,'')<>'invoice:void') FROM %ss o JOIN customers c ON c.id=o.customer_id AND c.company_id=o.company_id LEFT JOIN statuses s ON s.id=o.status_id WHERE o.id=$1 AND o.company_id=$2 FOR SHARE OF o,c`, d.DocumentType)
	if err = tx.QueryRow(ctx, q, d.DocumentID, d.CompanyID, d.DocumentType).Scan(&active); err != nil {
		return false, err
	}
	if !active {
		if err = allowTransitions(ctx, tx); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `UPDATE document_deliveries SET state='cancelled',cancelled_at=clock_timestamp(),last_error='Original document or Customer is archived, hidden, or Void',attempt_token=NULL,lease_expires_at=NULL WHERE id=$1 AND attempt_token=$2 AND state='sending' AND dispatch_boundary_known IS TRUE AND handoff_started_at IS NULL`, d.ID, d.AttemptToken); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	var id int64
	err = tx.QueryRow(ctx, `UPDATE document_deliveries SET handoff_started_at=clock_timestamp() WHERE id=$1 AND attempt_token=$2 AND state='sending' AND dispatch_boundary_known IS TRUE AND handoff_started_at IS NULL AND lease_expires_at>clock_timestamp() RETURNING id`, d.ID, d.AttemptToken).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = event(ctx, tx, d.ID, d.CompanyID, nil, "dispatch", "", "", "", "", map[string]any{"attempt_token": d.AttemptToken, "meaning": "transport dispatch admitted; acceptance unknown"}); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
