package services

import (
	"context"
	"errors"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

var errFinancialConservation = errors.New("financial source allocations do not reconcile")

func readFinancialCredit(ctx context.Context, tx pgx.Tx, p authorization.Principal, out *FinancialReport) error {
	// No activity-period predicate is allowed here. Current source availability
	// is calculated independently at the same caller-owned snapshot.
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.source_payment_id::text,c.customer_id,u.display_name,u.deleted_at IS NOT NULL,c.source_date::text,p.created_at,c.original_amount_cents,
 c.original_amount_cents
 -coalesce((SELECT sum(a.amount_cents) FROM credit_applications a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)),0)
 -coalesce((SELECT sum(a.amount_cents) FROM credit_refund_allocations a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_refund' AND r.operation_id=a.refund_id)),0)
 FROM customer_credits c JOIN customers u ON u.id=c.customer_id AND u.company_id=c.company_id JOIN invoice_payments p ON p.id=c.source_payment_id AND p.company_id=c.company_id
 WHERE c.company_id=$1 AND ($2::bigint=0 OR c.customer_id=$2) AND ($3 OR u.deleted_at IS NULL)
 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=c.source_payment_id)
 ORDER BY lower(u.display_name),u.id,nullif(c.source_date,'0001-01-01'::date) NULLS LAST,c.id`, p.CompanyID, out.Filter.CustomerID, out.Filter.IncludeArchived)
	if err != nil {
		return err
	}
	var sources []FinancialCreditRow
	for rows.Next() {
		var c FinancialCreditRow
		if err := rows.Scan(&c.ID, &c.PaymentID, &c.CustomerID, &c.Customer, &c.Archived, &c.SourceDate, &c.RecordedAt, &c.OriginalCents, &c.AvailableCents); err != nil {
			rows.Close()
			return err
		}
		c.SourceDate = financialSourceDate(c.SourceDate)
		sources = append(sources, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range sources {
		if c.AvailableCents < 0 || c.AvailableCents > c.OriginalCents {
			return errFinancialConservation
		}
		if c.AvailableCents == 0 {
			continue
		}
		c.Reference = authorization.FinancialReference{Facet: authorization.FinancialCredit, CustomerID: c.CustomerID, Archived: c.Archived, CreditID: c.ID}
		if err := authorization.CheckFinancialReference(ctx, tx, p, c.Reference); err != nil {
			return err
		}
		if err := financialAdd(&out.Totals.AvailableCreditCents, c.AvailableCents); err != nil {
			return err
		}
		out.Credits = append(out.Credits, c)
		out.References = append(out.References, c.Reference)
	}
	return nil
}
