package settlement

import (
	"context"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RefundReceipt retains only actor-owned disposition and exact effect identity.
// Reading it requires an active identity, never continued financial View access.
func (s *Service) RefundReceipt(ctx context.Context, a authorization.Actor, key string) (ApplicationReceipt, error) {
	out := ApplicationReceipt{OperationID: key}
	if a.Channel == "api" {
		return out, authorization.ErrForbidden
	}
	if _, err := uuid.Parse(key); err != nil {
		return out, authorization.ErrNotFound
	}
	err := authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		rows, err := tx.Query(ctx, `SELECT i.result_id,CASE i.operation WHEN 'refund_credit' THEN 'credit_refund' WHEN 'reverse_credit_refund' THEN 'credit_refund_reversal' ELSE 'payment_reversal' END FROM settlement_idempotency i LEFT JOIN credit_refunds f ON i.operation='refund_credit' AND f.id=i.result_id AND f.company_id=i.company_id LEFT JOIN settlement_reversals r ON i.operation IN ('reverse_credit_refund','reverse_payment') AND r.id=i.result_id AND r.company_id=i.company_id WHERE i.company_id=$1 AND i.idempotency_key=$2 AND coalesce(f.actor_id,r.actor_id)=$3 LIMIT 2`, p.CompanyID, key, p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			if rows.Err() != nil {
				return rows.Err()
			}
			return authorization.ErrNotFound
		}
		if err := rows.Scan(&out.EffectID, &out.EffectType); err != nil {
			return err
		}
		if rows.Next() {
			return ErrIdempotencyConflict
		}
		if rows.Err() != nil {
			return rows.Err()
		}
		out.Outcome = "accepted"
		return nil
	})
	return out, err
}

type RefundPage struct {
	CustomerID                                           int64
	Today                                                string
	CanRefund, CanReverseRefund, ShowHistory, ShowCredit bool
	AvailableCents                                       int64
	Refunds                                              []RefundEntry
	Payments                                             []RefundPayment
}
type RefundPayment struct {
	ID          uuid.UUID
	InvoiceID   int64
	AmountCents int64
	Date        time.Time
	Reversal    *ReversalEntry
	CanReverse  bool
	Key         string
}

func (s *Service) RefundContext(ctx context.Context, a authorization.Actor, customerID int64) (RefundPage, error) {
	out := RefundPage{CustomerID: customerID}
	if a.Channel == "api" {
		return out, authorization.ErrForbidden
	}
	err := authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "customer", customerID, ""); err != nil {
			return err
		}
		out.ShowCredit = p.Allows("credit.view", authorization.All)
		out.ShowHistory = p.Allows("settlement.view", authorization.All)
		if !out.ShowCredit && !out.ShowHistory {
			return authorization.ErrForbidden
		}
		var active bool
		var zone string
		if err := tx.QueryRow(ctx, `SELECT c.deleted_at IS NULL,s.timezone FROM customers c JOIN company_settings s ON s.company_id=c.company_id WHERE c.id=$1`, customerID).Scan(&active, &zone); err != nil {
			return err
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		out.Today = s.now().In(loc).Format("2006-01-02")
		out.CanRefund = active && out.ShowCredit && p.Allows("credit.refund", authorization.All)
		out.CanReverseRefund = active && out.ShowHistory && p.Allows("credit.refund.reverse", authorization.All)
		if out.ShowCredit {
			rows, err := tx.Query(ctx, creditAvailableSQL, p.CompanyID, customerID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id uuid.UUID
				var date time.Time
				var amount int64
				if err := rows.Scan(&id, &date, &amount); err != nil {
					rows.Close()
					return err
				}
				out.AvailableCents += amount
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		if !out.ShowHistory {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT f.id,f.amount_cents,f.method,f.effective_date,f.reference,f.notes,f.reason,r.id,r.effective_date,r.reason,coalesce(u.name,'Unknown historical actor'),f.created_at FROM credit_refunds f LEFT JOIN users u ON u.id=f.actor_id LEFT JOIN settlement_reversals r ON r.operation_type='credit_refund' AND r.operation_id=f.id WHERE f.company_id=$1 AND f.customer_id=$2 ORDER BY f.effective_date,f.id`, p.CompanyID, customerID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var f RefundEntry
			var id *uuid.UUID
			var date *time.Time
			var reason *string
			if err := rows.Scan(&f.ID, &f.AmountCents, &f.Method, &f.EffectiveDate, &f.Reference, &f.Notes, &f.Reason, &id, &date, &reason, &f.ActorName, &f.EnteredAt); err != nil {
				rows.Close()
				return err
			}
			if id != nil {
				f.Reversal = &ReversalEntry{ID: *id, EffectiveDate: *date, Reason: *reason}
			}
			f.ReversalKey = uuid.NewString()
			out.Refunds = append(out.Refunds, f)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for i := range out.Refunds {
			rows, err := tx.Query(ctx, `SELECT a.credit_id,a.amount_cents FROM credit_refund_allocations a JOIN customer_credits c ON c.id=a.credit_id WHERE a.refund_id=$1 ORDER BY c.source_date,c.id`, out.Refunds[i].ID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var portion Allocation
				if err := rows.Scan(&portion.SourceID, &portion.Cents); err != nil {
					rows.Close()
					return err
				}
				out.Refunds[i].Allocations = append(out.Refunds[i].Allocations, portion)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		rows, err = tx.Query(ctx, `SELECT p.id,coalesce(p.invoice_id,0),p.amount_cents,p.received_date,r.id,r.effective_date,r.reason,coalesce(r.reference,''),coalesce(r.notes,'') FROM invoice_payments p LEFT JOIN settlement_reversals r ON r.operation_type='payment' AND r.operation_id=p.id WHERE p.company_id=$1 AND p.customer_id=$2 ORDER BY p.received_date,p.id`, p.CompanyID, customerID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v RefundPayment
			var id *uuid.UUID
			var date *time.Time
			var reason *string
			var ref, note string
			if err := rows.Scan(&v.ID, &v.InvoiceID, &v.AmountCents, &v.Date, &id, &date, &reason, &ref, &note); err != nil {
				rows.Close()
				return err
			}
			if id != nil {
				v.Reversal = &ReversalEntry{ID: *id, EffectiveDate: *date, Reason: *reason, Reference: ref, Notes: note}
			}
			v.Key = uuid.NewString()
			out.Payments = append(out.Payments, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for i := range out.Payments {
			v := &out.Payments[i]
			v.CanReverse = active && p.Allows("payment.reverse", authorization.All) && v.Reversal == nil
			if v.CanReverse && v.InvoiceID != 0 {
				_, err := authorization.CheckParentTransaction(ctx, tx, p, "invoice", v.InvoiceID, "")
				v.CanReverse = err == nil
				if v.CanReverse {
					if err := tx.QueryRow(ctx, `SELECT i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND coalesce(s.category_key,'')!='invoice:void' FROM invoices i LEFT JOIN statuses s ON s.id=i.status_id WHERE i.id=$1`, v.InvoiceID).Scan(&v.CanReverse); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	return out, err
}
