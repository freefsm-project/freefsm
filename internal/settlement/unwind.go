package settlement

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type UnwindBlocker string

const (
	UnwindActivePaymentAllocation UnwindBlocker = "active_payment_allocation"
	UnwindActiveCreditApplication UnwindBlocker = "active_credit_application"
	UnwindUnresolvedPaymentCredit UnwindBlocker = "unresolved_payment_credit"
)

// RevertBlockers is transaction-compatible so conversion can make the unwind
// decision under the same invoice lock as its provenance transition.
func RevertBlockers(ctx context.Context, tx pgx.Tx, companyID, invoiceID int64) ([]UnwindBlocker, error) {
	var allocation, application, credit bool
	err := tx.QueryRow(ctx, `SELECT
EXISTS(SELECT 1 FROM payment_invoice_allocations a JOIN invoice_payments p ON p.id=a.payment_id WHERE p.company_id=$1 AND a.invoice_id=$2 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=p.id)),
EXISTS(SELECT 1 FROM credit_applications a WHERE a.company_id=$1 AND a.invoice_id=$2 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)),
EXISTS(SELECT 1 FROM customer_credits c JOIN invoice_payments p ON p.id=c.source_payment_id WHERE c.company_id=$1 AND p.invoice_id=$2 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=p.id))`, companyID, invoiceID).Scan(&allocation, &application, &credit)
	if err != nil {
		return nil, err
	}
	var out []UnwindBlocker
	if allocation {
		out = append(out, UnwindActivePaymentAllocation)
	}
	if application {
		out = append(out, UnwindActiveCreditApplication)
	}
	if credit {
		out = append(out, UnwindUnresolvedPaymentCredit)
	}
	return out, nil
}
