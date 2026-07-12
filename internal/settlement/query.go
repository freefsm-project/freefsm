package settlement

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type InvoiceSettlement struct {
	InvoiceID, CustomerID                    int64
	State                                    State
	TotalCents, SettledCents, AmountDueCents int64
	Payments                                 []PaymentEntry
	Applications                             []CreditApplicationEntry
}

type PaymentEntry struct {
	ID                                     uuid.UUID
	AmountCents, AppliedCents, CreditCents int64
	Method                                 PaymentMethod
	ReceivedDate                           time.Time
	Reference, Notes                       string
	Reversal                               *ReversalEntry
	ReversalKey                            string
}

type CreditApplicationEntry struct {
	ID, CreditID  uuid.UUID
	InvoiceID     int64
	AmountCents   int64
	EffectiveDate time.Time
	Reversal      *ReversalEntry
	ReversalKey   string
}

type ReversalEntry struct {
	ID            uuid.UUID
	EffectiveDate time.Time
	Reason        string
}

type CreditSource struct {
	ID, SourcePaymentID           uuid.UUID
	CustomerID, InvoiceID         int64
	SourceDate                    time.Time
	OriginalCents, AvailableCents int64
	PaymentMethod                 PaymentMethod
	PaymentReference              string
}

type RefundEntry struct {
	ID                       uuid.UUID
	CustomerID, AmountCents  int64
	Method                   PaymentMethod
	EffectiveDate            time.Time
	Reference, Notes, Reason string
	Allocations              []Allocation
	Reversal                 *ReversalEntry
	ReversalKey              string
}

type CustomerSettlement struct {
	CustomerID, AvailableCreditCents int64
	Sources                          []CreditSource
	Applications                     []CreditApplicationEntry
	Refunds                          []RefundEntry
}

func (s *Service) InvoiceSettlement(ctx context.Context, actor Actor, invoiceID int64) (InvoiceSettlement, error) {
	if err := authorize(actor); err != nil {
		return InvoiceSettlement{}, err
	}
	var out InvoiceSettlement
	var encoded, tax string
	err := s.db.QueryRow(ctx, `SELECT id,customer_id,settlement_state,line_items::text,tax_rate::text FROM invoices WHERE company_id=$1 AND id=$2 AND conversion_hidden_at IS NULL`, actor.CompanyID, invoiceID).Scan(&out.InvoiceID, &out.CustomerID, &out.State, &encoded, &tax)
	if err != nil {
		return out, err
	}
	out.TotalCents, err = invoiceTotal(encoded, tax)
	if err != nil {
		return out, err
	}
	rows, err := s.db.Query(ctx, `SELECT p.id,p.amount_cents,coalesce(a.amount_cents,0),p.amount_cents-coalesce(a.amount_cents,0),p.method,p.received_date,p.reference,p.notes,r.id,r.effective_date,r.reason FROM invoice_payments p LEFT JOIN payment_invoice_allocations a ON a.payment_id=p.id LEFT JOIN settlement_reversals r ON r.operation_type='payment' AND r.operation_id=p.id WHERE p.company_id=$1 AND p.invoice_id=$2 ORDER BY p.received_date,p.id`, actor.CompanyID, invoiceID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e PaymentEntry
		var rid *uuid.UUID
		var rd *time.Time
		var reason *string
		if err = rows.Scan(&e.ID, &e.AmountCents, &e.AppliedCents, &e.CreditCents, &e.Method, &e.ReceivedDate, &e.Reference, &e.Notes, &rid, &rd, &reason); err != nil {
			rows.Close()
			return out, err
		}
		if rid != nil {
			e.Reversal = &ReversalEntry{ID: *rid, EffectiveDate: *rd, Reason: *reason}
		}
		e.ReversalKey = uuid.NewString()
		out.Payments = append(out.Payments, e)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT a.id,a.credit_id,a.invoice_id,a.amount_cents,a.effective_date,r.id,r.effective_date,r.reason FROM credit_applications a LEFT JOIN settlement_reversals r ON r.operation_type='credit_application' AND r.operation_id=a.id WHERE a.company_id=$1 AND a.invoice_id=$2 ORDER BY a.effective_date,a.id`, actor.CompanyID, invoiceID)
	if err != nil {
		return out, err
	}
	out.Applications, err = scanApplications(rows)
	if err != nil {
		return out, err
	}
	for _, p := range out.Payments {
		if p.Reversal == nil {
			out.SettledCents += p.AppliedCents
		}
	}
	for _, a := range out.Applications {
		if a.Reversal == nil {
			out.SettledCents += a.AmountCents
		}
	}
	out.AmountDueCents = max(out.TotalCents-out.SettledCents, 0)
	return out, nil
}

func (s *Service) CustomerSettlement(ctx context.Context, actor Actor, customerID int64) (CustomerSettlement, error) {
	if err := authorize(actor); err != nil {
		return CustomerSettlement{}, err
	}
	out := CustomerSettlement{CustomerID: customerID}
	rows, err := s.db.Query(ctx, `SELECT c.id,c.source_payment_id,c.customer_id,p.invoice_id,c.source_date,c.original_amount_cents,available.amount_cents,p.method,p.reference FROM customer_credits c JOIN invoice_payments p ON p.id=c.source_payment_id JOIN invoices i ON i.id=p.invoice_id AND i.company_id=p.company_id AND i.conversion_hidden_at IS NULL CROSS JOIN LATERAL (SELECT c.original_amount_cents-coalesce((SELECT sum(a.amount_cents) FROM credit_applications a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)),0)-coalesce((SELECT sum(ra.amount_cents) FROM credit_refund_allocations ra WHERE ra.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_refund' AND r.operation_id=ra.refund_id)),0) amount_cents) available WHERE c.company_id=$1 AND c.customer_id=$2 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=c.source_payment_id) ORDER BY c.source_date,c.id`, actor.CompanyID, customerID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var source CreditSource
		if err = rows.Scan(&source.ID, &source.SourcePaymentID, &source.CustomerID, &source.InvoiceID, &source.SourceDate, &source.OriginalCents, &source.AvailableCents, &source.PaymentMethod, &source.PaymentReference); err != nil {
			rows.Close()
			return out, err
		}
		out.Sources = append(out.Sources, source)
		out.AvailableCreditCents += source.AvailableCents
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return out, err
	}
	rows.Close()
	rows, err = s.db.Query(ctx, `SELECT a.id,a.credit_id,a.invoice_id,a.amount_cents,a.effective_date,r.id,r.effective_date,r.reason FROM credit_applications a JOIN invoices i ON i.id=a.invoice_id AND i.company_id=a.company_id AND i.conversion_hidden_at IS NULL LEFT JOIN settlement_reversals r ON r.operation_type='credit_application' AND r.operation_id=a.id WHERE a.company_id=$1 AND a.customer_id=$2 ORDER BY a.effective_date,a.id`, actor.CompanyID, customerID)
	if err != nil {
		return out, err
	}
	out.Applications, err = scanApplications(rows)
	if err != nil {
		return out, err
	}
	rows, err = s.db.Query(ctx, `SELECT f.id,f.customer_id,f.amount_cents,f.method,f.effective_date,f.reference,f.notes,f.reason,r.id,r.effective_date,r.reason FROM credit_refunds f LEFT JOIN settlement_reversals r ON r.operation_type='credit_refund' AND r.operation_id=f.id WHERE f.company_id=$1 AND f.customer_id=$2 ORDER BY f.effective_date,f.id`, actor.CompanyID, customerID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var e RefundEntry
		var rid *uuid.UUID
		var rd *time.Time
		var rr *string
		if err = rows.Scan(&e.ID, &e.CustomerID, &e.AmountCents, &e.Method, &e.EffectiveDate, &e.Reference, &e.Notes, &e.Reason, &rid, &rd, &rr); err != nil {
			return out, err
		}
		if rid != nil {
			e.Reversal = &ReversalEntry{ID: *rid, EffectiveDate: *rd, Reason: *rr}
		}
		e.ReversalKey = uuid.NewString()
		arows, qerr := s.db.Query(ctx, `SELECT credit_id,amount_cents FROM credit_refund_allocations WHERE refund_id=$1 ORDER BY credit_id`, e.ID)
		if qerr != nil {
			return out, qerr
		}
		for arows.Next() {
			var a Allocation
			if qerr = arows.Scan(&a.SourceID, &a.Cents); qerr != nil {
				arows.Close()
				return out, qerr
			}
			e.Allocations = append(e.Allocations, a)
		}
		if qerr = arows.Err(); qerr != nil {
			arows.Close()
			return out, qerr
		}
		arows.Close()
		out.Refunds = append(out.Refunds, e)
	}
	return out, rows.Err()
}

func (s *Service) CustomerAvailableCredit(ctx context.Context, actor Actor, customerID int64) (int64, error) {
	view, err := s.CustomerSettlement(ctx, actor, customerID)
	return view.AvailableCreditCents, err
}

func scanApplications(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}) ([]CreditApplicationEntry, error) {
	defer rows.Close()
	var out []CreditApplicationEntry
	for rows.Next() {
		var e CreditApplicationEntry
		var rid *uuid.UUID
		var rd *time.Time
		var reason *string
		if err := rows.Scan(&e.ID, &e.CreditID, &e.InvoiceID, &e.AmountCents, &e.EffectiveDate, &rid, &rd, &reason); err != nil {
			return nil, err
		}
		if rid != nil {
			e.Reversal = &ReversalEntry{ID: *rid, EffectiveDate: *rd, Reason: *reason}
		}
		e.ReversalKey = uuid.NewString()
		out = append(out, e)
	}
	return out, rows.Err()
}
