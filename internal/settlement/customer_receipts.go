package settlement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ReceiptInvoiceChoice struct {
	ID       int64
	Label    string
	DueCents int64
}
type CustomerReceiptEntry struct {
	PaymentEntry
	InvoiceID      int64
	SourceCreditID *uuid.UUID
	ActorName      string
	EnteredAt      *time.Time
}
type CustomerReceiptPage struct {
	CustomerID                         int64
	CustomerName, Today                string
	CanRecord, CanReverse, ShowHistory bool
	Methods                            []MethodChoice
	Invoices                           []ReceiptInvoiceChoice
	Receipts                           []CustomerReceiptEntry
}

// CustomerReceipts keeps action context, settlement history and available Credit
// separate. Initial origin Credit is part of receipt reconciliation, not cash.
func (s *Service) CustomerReceipts(ctx context.Context, a authorization.Actor, customerID int64) (CustomerReceiptPage, error) {
	out := CustomerReceiptPage{CustomerID: customerID}
	if a.Channel == "api" {
		return out, authorization.ErrForbidden
	}
	err := authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "customer", customerID, ""); err != nil {
			return err
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT display_name,deleted_at IS NULL FROM customers WHERE id=$1`, customerID).Scan(&out.CustomerName, &active); err != nil {
			return err
		}
		out.CanRecord = active && p.Allows("payment.credit", authorization.All) && p.Allows("customer.financial.view", authorization.All)
		out.ShowHistory = p.Allows("settlement.view", authorization.All)
		out.CanReverse = active && out.ShowHistory && p.Allows("payment.reverse", authorization.All)
		if !out.CanRecord && !out.ShowHistory {
			return authorization.ErrForbidden
		}
		var zone string
		if err := tx.QueryRow(ctx, `SELECT timezone FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&zone); err != nil {
			return err
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		out.Today = s.now().In(loc).Format("2006-01-02")
		if out.CanRecord {
			out.Methods, err = methodChoices(ctx, tx, p.CompanyID)
			if err != nil {
				return err
			}
			rows, err := tx.Query(ctx, `SELECT i.id,coalesce(nullif(i.number_label,''),i.invoice_number::text,i.id::text),i.line_items::text,i.tax_rate::text,i.financial FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.company_id=$1 AND i.customer_id=$2 AND i.deleted_at IS NULL AND i.conversion_hidden_at IS NULL AND s.category_key IN ('invoice:invoiced','invoice:sent') AND ($3 OR ($4 AND EXISTS(SELECT 1 FROM jobs j JOIN job_assignments a ON a.job_id=j.id WHERE j.id=i.job_id AND j.deleted_at IS NULL AND a.user_id=$5))) ORDER BY i.id`, p.CompanyID, customerID, p.Allows("invoice.view", authorization.All), p.Allows("invoice.view", authorization.Assigned), p.UserID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var choice ReceiptInvoiceChoice
				var lines, tax, financial string
				if err := rows.Scan(&choice.ID, &choice.Label, &lines, &tax, &financial); err != nil {
					rows.Close()
					return err
				}
				choice.DueCents, err = invoiceTotal(lines, tax, financial)
				if err != nil {
					rows.Close()
					return err
				}
				out.Invoices = append(out.Invoices, choice)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
			eligible := out.Invoices[:0]
			for _, choice := range out.Invoices {
				paid, err := activeSettled(ctx, tx, choice.ID)
				if err != nil {
					return err
				}
				choice.DueCents -= paid
				if choice.DueCents > 0 {
					eligible = append(eligible, choice)
				}
			}
			out.Invoices = eligible
		}
		if !out.ShowHistory {
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT p.id,coalesce(p.invoice_id,0),p.amount_cents,coalesce(a.amount_cents,0),coalesce(c.original_amount_cents,0),c.id,p.method,coalesce(p.method_label,p.method),coalesce(p.other_description,''),p.received_date,p.reference,p.notes,coalesce(u.name,'Unknown historical actor'),p.created_at,coalesce(p.channel,'unknown'),r.id,r.effective_date,r.reason FROM invoice_payments p LEFT JOIN payment_invoice_allocations a ON a.payment_id=p.id LEFT JOIN customer_credits c ON c.source_payment_id=p.id LEFT JOIN users u ON u.id=p.actor_id LEFT JOIN settlement_reversals r ON r.operation_type='payment' AND r.operation_id=p.id WHERE p.company_id=$1 AND p.customer_id=$2 ORDER BY p.received_date DESC,p.id`, p.CompanyID, customerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var e CustomerReceiptEntry
			var rid *uuid.UUID
			var date *time.Time
			var reason *string
			if err := rows.Scan(&e.ID, &e.InvoiceID, &e.AmountCents, &e.AppliedCents, &e.CreditCents, &e.SourceCreditID, &e.Method, &e.MethodLabel, &e.OtherDescription, &e.ReceivedDate, &e.Reference, &e.Notes, &e.ActorName, &e.EnteredAt, &e.Channel, &rid, &date, &reason); err != nil {
				return err
			}
			if rid != nil {
				e.Reversal = &ReversalEntry{ID: *rid, EffectiveDate: *date, Reason: *reason}
			}
			e.ReversalKey = uuid.NewString()
			out.Receipts = append(out.Receipts, e)
		}
		return rows.Err()
	})
	return out, err
}

// ReverseUnappliedPayment preserves the installed dependency and compensating
// record rules. It cannot reverse another Customer's or an Invoice's receipt.
func (s *Service) ReverseUnappliedPayment(ctx context.Context, a authorization.Actor, req ReverseRequest) (Result, error) {
	var out Result
	if a.Channel == "api" {
		return out, authorization.ErrForbidden
	}
	if id, err := uuid.Parse(req.Operation.Key); err != nil || id == uuid.Nil || req.CustomerID <= 0 || req.InvoiceID != 0 || strings.TrimSpace(req.Reason) == "" {
		return out, ErrInvalidAmount
	}
	err := authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		if !p.Allows("payment.reverse", authorization.All) || !p.Allows("settlement.view", authorization.All) {
			return authorization.ErrForbidden
		}
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "customer", req.CustomerID, "payment.reverse"); err != nil {
			return err
		}
		if err := authorization.AdmitOperation(ctx, tx, a, req.Operation.Key, "payment.reverse", req); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT result_json FROM settlement_idempotency WHERE company_id=$1 AND operation='reverse_payment' AND idempotency_key=$2`, p.CompanyID, req.Operation.Key).Scan(&raw)
		if err == nil {
			return json.Unmarshal(raw, &out)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var zone string
		if err := tx.QueryRow(ctx, `SELECT timezone FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&zone); err != nil {
			return err
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		out, err = reverseSettlement(ctx, tx, Actor{ID: p.UserID, CompanyID: p.CompanyID}, "payment", req, s.now().In(loc), true)
		if err != nil {
			return err
		}
		if err := protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: p.UserID, RoleID: p.RoleID, Action: "payment.reverse", Scope: string(authorization.All), TargetType: "customer", TargetID: req.CustomerID, Channel: a.Channel, SessionHash: a.SessionHash, Detail: map[string]any{"payment_id": req.ID, "reversal_id": out.ID, "operation_id": req.Operation.Key}}); err != nil {
			return err
		}
		raw, err = json.Marshal(out)
		if err != nil {
			return err
		}
		canonical, err := semanticJSON(req)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(canonical)
		_, err = tx.Exec(ctx, `INSERT INTO settlement_idempotency(company_id,operation,idempotency_key,request_fingerprint,result_id,result_json) VALUES($1,'reverse_payment',$2,$3,$4,$5)`, p.CompanyID, req.Operation.Key, hex.EncodeToString(sum[:]), out.ID, raw)
		return err
	})
	return out, err
}
