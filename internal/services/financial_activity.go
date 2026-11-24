package services

import (
	"context"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

// Each immutable cash event occurs once, with its own recording instant. Never
// join source allocations into this relation: a multi-source refund is one event.
const recordedCashEventsSQL = `
 SELECT 'payment'::text kind,p.id::text id,''::text original_kind,''::text original_id,p.company_id,p.customer_id,p.id::text payment_id,''::text credit_id,coalesce(p.invoice_id,0) invoice_id,p.created_at recorded_at,p.received_date::text effective_date,p.amount_cents,p.amount_cents net_cents FROM invoice_payments p
 UNION ALL
 SELECT 'payment_reversal',r.id::text,'payment',p.id::text,p.company_id,p.customer_id,p.id::text,'',coalesce(p.invoice_id,0),r.created_at,r.effective_date::text,p.amount_cents,-p.amount_cents FROM settlement_reversals r JOIN invoice_payments p ON r.operation_type='payment' AND p.id=r.operation_id AND p.company_id=r.company_id
 UNION ALL
 SELECT 'credit_refund',f.id::text,'','',f.company_id,f.customer_id,'','',0,f.created_at,f.effective_date::text,f.amount_cents,-f.amount_cents FROM credit_refunds f
 UNION ALL
 SELECT 'refund_reversal',r.id::text,'credit_refund',f.id::text,f.company_id,f.customer_id,'','',0,r.created_at,r.effective_date::text,f.amount_cents,f.amount_cents FROM settlement_reversals r JOIN credit_refunds f ON r.operation_type='credit_refund' AND f.id=r.operation_id AND f.company_id=r.company_id`

const recordedCreditEventsSQL = `
 UNION ALL
 SELECT 'credit_creation',c.id::text,'','',c.company_id,c.customer_id,p.id::text,c.id::text,coalesce(p.invoice_id,0),p.created_at,c.source_date::text,c.original_amount_cents,0 FROM customer_credits c JOIN invoice_payments p ON p.id=c.source_payment_id AND p.company_id=c.company_id
 UNION ALL
 SELECT 'credit_application',a.id::text,'','',a.company_id,a.customer_id,c.source_payment_id::text,c.id::text,a.invoice_id,a.created_at,a.effective_date::text,a.amount_cents,0 FROM credit_applications a JOIN customer_credits c ON c.id=a.credit_id AND c.company_id=a.company_id
 UNION ALL
 SELECT 'application_reversal',r.id::text,'credit_application',a.id::text,a.company_id,a.customer_id,c.source_payment_id::text,c.id::text,a.invoice_id,r.created_at,r.effective_date::text,a.amount_cents,0 FROM settlement_reversals r JOIN credit_applications a ON r.operation_type='credit_application' AND a.id=r.operation_id AND a.company_id=r.company_id JOIN customer_credits c ON c.id=a.credit_id AND c.company_id=a.company_id`

type FinancialQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// RecordedNetPayments is a monetary primitive, not an authorization bypass for
// callers. Its company and half-open bounds must come from authorized context.
// The report uses the same relation, then filters Customers before its totals.
func RecordedNetPayments(ctx context.Context, q FinancialQueryer, companyID int64, from, until time.Time) (int64, error) {
	var cents int64
	err := q.QueryRow(ctx, `WITH events AS (`+recordedCashEventsSQL+`) SELECT coalesce(sum(net_cents),0) FROM events WHERE company_id=$1 AND recorded_at >= $2 AND recorded_at < $3`, companyID, from, until).Scan(&cents)
	return cents, err
}

func readFinancialActivity(ctx context.Context, tx pgx.Tx, p authorization.Principal, out *FinancialReport, loc *time.Location) error {
	from, until, err := financialDateBounds(out.Filter, loc)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `WITH events AS (`+recordedCashEventsSQL+recordedCreditEventsSQL+`)
 SELECT e.kind,e.id,e.original_kind,e.original_id,e.customer_id,c.display_name,c.deleted_at IS NOT NULL,e.payment_id,e.credit_id,e.invoice_id,e.recorded_at,e.effective_date,e.amount_cents,e.net_cents
 FROM events e JOIN customers c ON c.id=e.customer_id AND c.company_id=e.company_id
 WHERE e.company_id=$1 AND ($2::bigint=0 OR e.customer_id=$2) AND ($3 OR c.deleted_at IS NULL)
 AND (e.recorded_at IS NULL OR (($4::timestamptz IS NULL OR e.recorded_at >= $4) AND ($5::timestamptz IS NULL OR e.recorded_at < $5)))
 ORDER BY e.recorded_at NULLS LAST,e.kind,e.id`, p.CompanyID, out.Filter.CustomerID, out.Filter.IncludeArchived, from, until)
	if err != nil {
		return err
	}
	var events []FinancialActivityRow
	for rows.Next() {
		var e FinancialActivityRow
		if err := rows.Scan(&e.Kind, &e.ID, &e.OriginalKind, &e.OriginalID, &e.CustomerID, &e.Customer, &e.Archived, &e.PaymentID, &e.CreditID, &e.InvoiceID, &e.RecordedAt, &e.EffectiveDate, &e.AmountCents, &e.NetPaymentsCents); err != nil {
			rows.Close()
			return err
		}
		e.Reference = authorization.FinancialReference{Facet: authorization.FinancialSettlement, CustomerID: e.CustomerID, Archived: e.Archived, EventKind: e.Kind, EventID: e.ID}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range events {
		if err := authorization.CheckFinancialReference(ctx, tx, p, e.Reference); err != nil {
			return err
		}
		if e.Kind == "credit_refund" || e.Kind == "refund_reversal" {
			id := e.ID
			if e.Kind == "refund_reversal" {
				id = e.OriginalID
			}
			e.Allocations, err = financialRefundSources(ctx, tx, p.CompanyID, e.CustomerID, id)
			if err != nil {
				return err
			}
			var total int64
			for _, a := range e.Allocations {
				if err := financialAdd(&total, a.AmountCents); err != nil {
					return err
				}
			}
			if total != e.AmountCents {
				return errFinancialConservation
			}
			for i := range e.Allocations {
				e.Allocations[i].Kind = "refund_credit"
				e.Allocations[i].ID = id
				if e.Kind == "refund_reversal" {
					e.Allocations[i].AmountCents = -e.Allocations[i].AmountCents
				}
			}
		} else if e.Kind == "payment" || e.Kind == "payment_reversal" || e.Kind == "credit_application" || e.Kind == "application_reversal" {
			e.Allocations, err = financialEventAllocations(ctx, tx, p.CompanyID, e)
			if err != nil {
				return err
			}
		}
		if e.RecordedAt == nil {
			out.UnknownActivity = append(out.UnknownActivity, e)
			out.Totals.UnknownEventCount++
			if err := financialAdd(&out.Totals.UnknownAmountCents, e.AmountCents); err != nil {
				return err
			}
			if err := financialAdd(&out.Totals.UnknownNetPaymentsCents, e.NetPaymentsCents); err != nil {
				return err
			}
		} else {
			out.Activity = append(out.Activity, e)
			if err := financialAdd(&out.Totals.NetPaymentsCents, e.NetPaymentsCents); err != nil {
				return err
			}
		}
		out.References = append(out.References, e.Reference)
	}
	return nil
}

func financialEventAllocations(ctx context.Context, tx pgx.Tx, company int64, e FinancialActivityRow) ([]FinancialSourceAllocation, error) {
	var query, kind string
	id := e.ID
	reversed := e.Kind == "payment_reversal" || e.Kind == "application_reversal"
	if reversed {
		id = e.OriginalID
	}
	if e.Kind == "payment" || e.Kind == "payment_reversal" {
		kind = "payment_invoice"
		query = `SELECT ''::text,p.id::text,a.invoice_id,p.received_date::text,a.amount_cents FROM payment_invoice_allocations a JOIN invoice_payments p ON p.id=a.payment_id WHERE p.company_id=$1 AND p.customer_id=$2 AND p.id::text=$3`
	} else {
		kind = "credit_application"
		query = `SELECT c.id::text,c.source_payment_id::text,a.invoice_id,c.source_date::text,a.amount_cents FROM credit_applications a JOIN customer_credits c ON c.id=a.credit_id AND c.company_id=a.company_id WHERE a.company_id=$1 AND a.customer_id=$2 AND a.id::text=$3`
	}
	rows, err := tx.Query(ctx, query, company, e.CustomerID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FinancialSourceAllocation
	for rows.Next() {
		a := FinancialSourceAllocation{Kind: kind, ID: id}
		if err := rows.Scan(&a.CreditID, &a.PaymentID, &a.InvoiceID, &a.SourceDate, &a.AmountCents); err != nil {
			return nil, err
		}
		a.SourceDate = financialSourceDate(a.SourceDate)
		if reversed {
			a.AmountCents = -a.AmountCents
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func financialRefundSources(ctx context.Context, tx pgx.Tx, company, customer int64, id string) ([]FinancialSourceAllocation, error) {
	rows, err := tx.Query(ctx, `SELECT c.id::text,c.source_payment_id::text,c.source_date::text,a.amount_cents FROM credit_refund_allocations a JOIN customer_credits c ON c.id=a.credit_id JOIN credit_refunds f ON f.id=a.refund_id AND f.company_id=c.company_id AND f.customer_id=c.customer_id WHERE f.id::text=$1 AND c.company_id=$2 AND c.customer_id=$3 ORDER BY nullif(c.source_date,'0001-01-01'::date) NULLS LAST,c.id`, id, company, customer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []FinancialSourceAllocation
	for rows.Next() {
		var a FinancialSourceAllocation
		if err := rows.Scan(&a.CreditID, &a.PaymentID, &a.SourceDate, &a.AmountCents); err != nil {
			return nil, err
		}
		a.SourceDate = financialSourceDate(a.SourceDate)
		result = append(result, a)
	}
	return result, rows.Err()
}
