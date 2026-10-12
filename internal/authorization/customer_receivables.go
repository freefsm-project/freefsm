package authorization

import (
	"context"
	"time"
)

// ReceivableSource includes only inputs for authorized active issued Invoices.
// Canonical monetary arithmetic is performed by the shared pricing module.
type ReceivableSource struct {
	ID                 int64
	DueDate            *time.Time
	LineItems, TaxRate string
	SettledCents       int64
}

func (s *Service) CustomerReceivableSources(ctx context.Context, a Actor, customerID int64) ([]ReceivableSource, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return nil, err
	}
	if _, err = childCustomer(ctx, s.Pool, p, customerID, false); err != nil {
		return nil, err
	}
	if !p.Allows("customer.financial.view", All) {
		return nil, ErrForbidden
	}
	rows, err := s.Pool.Query(ctx, `SELECT i.id,i.due_date,i.line_items::text,i.tax_rate::text,
 coalesce((SELECT sum(x.amount_cents) FROM payment_invoice_allocations x JOIN invoice_payments p ON p.id=x.payment_id WHERE x.invoice_id=i.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=p.id)),0)
 +coalesce((SELECT sum(x.amount_cents) FROM credit_applications x WHERE x.invoice_id=i.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=x.id)),0)
 FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.customer_id=$1 AND i.company_id=$2 AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND s.category_key NOT IN ('invoice:draft','invoice:void') AND ($4 OR ($5 AND EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=i.job_id AND j.company_id=$2 AND j.customer_id=$1 AND j.deleted_at IS NULL AND a.user_id=$3))) ORDER BY i.id`, customerID, p.CompanyID, a.UserID, p.Allows("invoice.view", All), p.Allows("invoice.view", Assigned))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ReceivableSource{}
	for rows.Next() {
		var row ReceivableSource
		if err = rows.Scan(&row.ID, &row.DueDate, &row.LineItems, &row.TaxRate, &row.SettledCents); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (s *Service) CustomerCreditTotal(ctx context.Context, a Actor, customerID int64) (int64, error) {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return 0, err
	}
	if _, err = childCustomer(ctx, s.Pool, p, customerID, false); err != nil {
		return 0, err
	}
	if !p.Allows("credit.view", All) {
		return 0, ErrForbidden
	}
	var total int64
	err = s.Pool.QueryRow(ctx, `SELECT coalesce(sum(c.original_amount_cents
 -coalesce((SELECT sum(x.amount_cents) FROM credit_applications x WHERE x.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=x.id)),0)
 -coalesce((SELECT sum(x.amount_cents) FROM credit_refund_allocations x WHERE x.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_refund' AND r.operation_id=x.refund_id)),0)),0)
 FROM customer_credits c WHERE c.customer_id=$1 AND c.company_id=$2 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=c.source_payment_id)`, customerID, p.CompanyID).Scan(&total)
	return total, err
}
