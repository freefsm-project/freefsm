// Package settlement owns invoice settlement and customer-credit invariants.
package settlement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrForbidden           = errors.New("settlement operation forbidden")
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")
	ErrArchived            = errors.New("archived invoice cannot be settled")
	ErrVoid                = errors.New("void invoice cannot be settled")
	ErrDependency          = errors.New("operation has active dependants")
	ErrNotFound            = errors.New("settlement record not found")
)

type Actor struct {
	ID, CompanyID int64
	Role          string
}
type Operation struct {
	Key string
}
type Date string

type PaymentMethod string

const (
	Cash       PaymentMethod = "cash"
	Check      PaymentMethod = "check"
	CreditCard PaymentMethod = "credit_card"
	Transfer   PaymentMethod = "transfer"
	Other      PaymentMethod = "other"
)

type RecordPaymentRequest struct {
	Operation              Operation
	InvoiceID, AmountCents int64
	Method                 PaymentMethod
	ReceivedDate           Date
	Reference, Notes       string
}
type ApplyCreditRequest struct {
	Operation      Operation
	InvoiceID      int64
	CreditID       uuid.UUID
	RequestedCents int64
	EffectiveDate  Date
}
type RefundCreditRequest struct {
	Operation                Operation
	CustomerID, AmountCents  int64
	Method                   PaymentMethod
	EffectiveDate            Date
	Reference, Notes, Reason string
}
type ReverseRequest struct {
	Operation     Operation
	ID            uuid.UUID
	InvoiceID     int64
	CustomerID    int64
	EffectiveDate Date
	Reason        string
}
type Result struct {
	ID                        uuid.UUID
	AppliedCents, CreditCents int64
	State                     State
}

type Service struct {
	db  *pgxpool.Pool
	now func() time.Time
}

func New(db *pgxpool.Pool) *Service { return &Service{db: db, now: time.Now} }

func (s *Service) RecordPayment(ctx context.Context, actor Actor, req RecordPaymentRequest) (Result, error) {
	if err := authorize(actor); err != nil {
		return Result{}, err
	}
	if req.AmountCents <= 0 || !validMethod(req.Method) {
		return Result{}, ErrInvalidAmount
	}
	return s.transact(ctx, actor, "record_payment", req.Operation, req, func(tx pgx.Tx, today time.Time) (Result, error) {
		inv, err := lockInvoice(ctx, tx, actor.CompanyID, req.InvoiceID)
		if err != nil {
			return Result{}, err
		}
		if err := lockCustomer(ctx, tx, actor.CompanyID, inv.customerID); err != nil {
			return Result{}, err
		}
		receivedDate, err := parseRequestDate(req.ReceivedDate, today.Location())
		if err != nil {
			return Result{}, err
		}
		if err := ValidateEffectiveDate(receivedDate, today, time.Time{}); err != nil {
			return Result{}, err
		}
		total, err := invoiceTotal(inv.lineItems, inv.taxRate)
		if err != nil {
			return Result{}, err
		}
		settled, err := activeSettled(ctx, tx, inv.id)
		if err != nil {
			return Result{}, err
		}
		applied, credit, err := SplitPayment(req.AmountCents, max(total-settled, 0))
		if err != nil {
			return Result{}, err
		}
		id := uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,invoice_id,amount_cents,method,received_date,reference,notes,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, actor.CompanyID, inv.customerID, inv.id, req.AmountCents, req.Method, receivedDate, req.Reference, req.Notes, actor.ID)
		if err != nil {
			return Result{}, err
		}
		if applied > 0 {
			if _, err = tx.Exec(ctx, `INSERT INTO payment_invoice_allocations VALUES($1,$2,$3)`, id, inv.id, applied); err != nil {
				return Result{}, err
			}
		}
		if credit > 0 {
			_, err = tx.Exec(ctx, `INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date) VALUES($1,$2,$3,$4,$5,$6)`, uuid.New(), actor.CompanyID, inv.customerID, id, credit, receivedDate)
			if err != nil {
				return Result{}, err
			}
		}
		state, _ := StateFor(total, settled+applied)
		if err = projectAndLog(ctx, tx, actor, inv.id, state, "payment_recorded", id); err != nil {
			return Result{}, err
		}
		return Result{ID: id, AppliedCents: applied, CreditCents: credit, State: state}, nil
	})
}

func (s *Service) ApplyCredit(ctx context.Context, actor Actor, req ApplyCreditRequest) (Result, error) {
	if err := authorize(actor); err != nil {
		return Result{}, err
	}
	if req.RequestedCents <= 0 {
		return Result{}, ErrInvalidAmount
	}
	return s.transact(ctx, actor, "apply_credit", req.Operation, req, func(tx pgx.Tx, today time.Time) (Result, error) {
		inv, err := lockInvoice(ctx, tx, actor.CompanyID, req.InvoiceID)
		if err != nil {
			return Result{}, err
		}
		if err = lockCustomer(ctx, tx, actor.CompanyID, inv.customerID); err != nil {
			return Result{}, err
		}
		var sourceDate time.Time
		var available int64
		var sourceID uuid.UUID
		err = tx.QueryRow(ctx, creditAvailableSQL+` AND c.id=$3 FOR UPDATE OF c`, actor.CompanyID, inv.customerID, req.CreditID).Scan(&sourceID, &sourceDate, &available)
		if err != nil {
			return Result{}, err
		}
		effectiveDate, err := parseRequestDate(req.EffectiveDate, today.Location())
		if err != nil {
			return Result{}, err
		}
		if err = ValidateEffectiveDate(effectiveDate, today, sourceDate); err != nil {
			return Result{}, err
		}
		total, err := invoiceTotal(inv.lineItems, inv.taxRate)
		if err != nil {
			return Result{}, err
		}
		settled, err := activeSettled(ctx, tx, inv.id)
		if err != nil {
			return Result{}, err
		}
		amount := min(req.RequestedCents, min(available, max(total-settled, 0)))
		if amount <= 0 {
			return Result{}, ErrInvalidAmount
		}
		id := uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO credit_applications(id,company_id,customer_id,invoice_id,credit_id,amount_cents,effective_date,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, id, actor.CompanyID, inv.customerID, inv.id, req.CreditID, amount, effectiveDate, actor.ID)
		if err != nil {
			return Result{}, err
		}
		state, _ := StateFor(total, settled+amount)
		if err = projectAndLog(ctx, tx, actor, inv.id, state, "credit_applied", id); err != nil {
			return Result{}, err
		}
		return Result{ID: id, AppliedCents: amount, State: state}, nil
	})
}

func (s *Service) RefundCredit(ctx context.Context, actor Actor, req RefundCreditRequest) (Result, error) {
	if err := authorize(actor); err != nil {
		return Result{}, err
	}
	if req.AmountCents <= 0 || !validMethod(req.Method) || strings.TrimSpace(req.Reason) == "" {
		return Result{}, ErrInvalidAmount
	}
	return s.transact(ctx, actor, "refund_credit", req.Operation, req, func(tx pgx.Tx, today time.Time) (Result, error) {
		if err := lockCustomer(ctx, tx, actor.CompanyID, req.CustomerID); err != nil {
			return Result{}, err
		}
		rows, err := tx.Query(ctx, creditAvailableSQL+` AND available.amount_cents>0 ORDER BY c.source_date,c.id FOR UPDATE`, actor.CompanyID, req.CustomerID)
		if err != nil {
			return Result{}, err
		}
		defer rows.Close()
		var sources []AvailableSource
		dates := make(map[string]time.Time)
		for rows.Next() {
			var id uuid.UUID
			var d time.Time
			var cents int64
			if err = rows.Scan(&id, &d, &cents); err != nil {
				return Result{}, err
			}
			sources = append(sources, AvailableSource{ID: id.String(), Date: d, Cents: cents})
			dates[id.String()] = d
		}
		if err = rows.Err(); err != nil {
			return Result{}, err
		}
		allocs, err := AllocateFIFO(req.AmountCents, sources)
		if err != nil {
			return Result{}, err
		}
		var newestConsumed time.Time
		for _, allocation := range allocs {
			if dates[allocation.SourceID].After(newestConsumed) {
				newestConsumed = dates[allocation.SourceID]
			}
		}
		effectiveDate, err := parseRequestDate(req.EffectiveDate, today.Location())
		if err != nil {
			return Result{}, err
		}
		if err = ValidateEffectiveDate(effectiveDate, today, newestConsumed); err != nil {
			return Result{}, err
		}
		id := uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO credit_refunds(id,company_id,customer_id,amount_cents,method,effective_date,reference,notes,reason,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, actor.CompanyID, req.CustomerID, req.AmountCents, req.Method, effectiveDate, req.Reference, req.Notes, req.Reason, actor.ID)
		if err != nil {
			return Result{}, err
		}
		for _, a := range allocs {
			if _, err = tx.Exec(ctx, `INSERT INTO credit_refund_allocations VALUES($1,$2,$3)`, id, a.SourceID, a.Cents); err != nil {
				return Result{}, err
			}
		}
		if err = logActivity(ctx, tx, actor, "customer", req.CustomerID, "credit_refunded", id); err != nil {
			return Result{}, err
		}
		return Result{ID: id}, nil
	})
}

func (s *Service) ReversePayment(ctx context.Context, a Actor, r ReverseRequest) (Result, error) {
	return s.reverse(ctx, a, "payment", r, true)
}
func (s *Service) ReverseCreditApplication(ctx context.Context, a Actor, r ReverseRequest) (Result, error) {
	return s.reverse(ctx, a, "credit_application", r, false)
}
func (s *Service) ReverseRefund(ctx context.Context, a Actor, r ReverseRequest) (Result, error) {
	return s.reverse(ctx, a, "credit_refund", r, false)
}

func (s *Service) reverse(ctx context.Context, actor Actor, kind string, req ReverseRequest, checkDeps bool) (Result, error) {
	if err := authorize(actor); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return Result{}, errors.New("reversal reason is required")
	}
	return s.transact(ctx, actor, "reverse_"+kind, req.Operation, req, func(tx pgx.Tx, today time.Time) (Result, error) {
		effectiveDate, err := parseRequestDate(req.EffectiveDate, today.Location())
		if err != nil {
			return Result{}, err
		}
		if err := ValidateEffectiveDate(effectiveDate, today, time.Time{}); err != nil {
			return Result{}, err
		}
		var invoiceID, customerID int64
		var originalDate time.Time
		query := map[string]string{"payment": `SELECT invoice_id,customer_id,received_date FROM invoice_payments WHERE company_id=$1 AND id=$2 AND invoice_id=$3`, "credit_application": `SELECT invoice_id,customer_id,effective_date FROM credit_applications WHERE company_id=$1 AND id=$2 AND invoice_id=$3`, "credit_refund": `SELECT 0,customer_id,effective_date FROM credit_refunds WHERE company_id=$1 AND id=$2 AND customer_id=$3`}[kind]
		parentID := req.InvoiceID
		if kind == "credit_refund" {
			parentID = req.CustomerID
		}
		if err := tx.QueryRow(ctx, query, actor.CompanyID, req.ID, parentID).Scan(&invoiceID, &customerID, &originalDate); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Result{}, ErrNotFound
			}
			return Result{}, err
		}
		if effectiveDate.Before(dateOnly(originalDate)) {
			return Result{}, errors.New("reversal date precedes original operation")
		}
		if invoiceID > 0 {
			inv, err := lockInvoice(ctx, tx, actor.CompanyID, invoiceID)
			if err != nil {
				return Result{}, err
			}
			if err = lockCustomer(ctx, tx, actor.CompanyID, inv.customerID); err != nil {
				return Result{}, err
			}
		}
		// Lock every affected source after invoice/customer, preserving the global order.
		var lockRows pgx.Rows
		switch kind {
		case "payment":
			lockRows, err = tx.Query(ctx, `SELECT c.id FROM customer_credits c WHERE c.source_payment_id=$1 ORDER BY c.source_date,c.id FOR UPDATE`, req.ID)
		case "credit_application":
			lockRows, err = tx.Query(ctx, `SELECT c.id FROM customer_credits c JOIN credit_applications a ON a.credit_id=c.id WHERE a.id=$1 ORDER BY c.source_date,c.id FOR UPDATE OF c`, req.ID)
		case "credit_refund":
			err = lockCustomer(ctx, tx, actor.CompanyID, customerID)
			if err == nil {
				lockRows, err = tx.Query(ctx, `SELECT c.id FROM customer_credits c JOIN credit_refund_allocations a ON a.credit_id=c.id WHERE a.refund_id=$1 ORDER BY c.source_date,c.id FOR UPDATE OF c`, req.ID)
			}
		}
		if err != nil {
			return Result{}, err
		}
		if lockRows != nil {
			for lockRows.Next() {
				var ignored uuid.UUID
				if err = lockRows.Scan(&ignored); err != nil {
					return Result{}, err
				}
			}
			if err = lockRows.Err(); err != nil {
				return Result{}, err
			}
			lockRows.Close()
		}
		if checkDeps {
			var dependent bool
			err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customer_credits c WHERE c.source_payment_id=$1 AND (EXISTS(SELECT 1 FROM credit_applications a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)) OR EXISTS(SELECT 1 FROM credit_refund_allocations ra JOIN credit_refunds f ON f.id=ra.refund_id WHERE ra.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_refund' AND r.operation_id=f.id))))`, req.ID).Scan(&dependent)
			if err != nil {
				return Result{}, err
			}
			if dependent {
				return Result{}, ErrDependency
			}
		}
		id := uuid.New()
		_, err = tx.Exec(ctx, `INSERT INTO settlement_reversals(id,company_id,operation_type,operation_id,reason,actor_id,effective_date) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, actor.CompanyID, kind, req.ID, req.Reason, actor.ID, effectiveDate)
		if err != nil {
			return Result{}, err
		}
		if invoiceID > 0 {
			if err = reproject(ctx, tx, actor, invoiceID, "settlement_reversed", id); err != nil {
				return Result{}, err
			}
		} else {
			if err = logActivity(ctx, tx, actor, "customer", customerID, "credit_refund_reversed", id); err != nil {
				return Result{}, err
			}
		}
		return Result{ID: id}, nil
	})
}

type lockedInvoice struct {
	id, customerID     int64
	lineItems, taxRate string
}

func lockInvoice(ctx context.Context, tx pgx.Tx, companyID, id int64) (lockedInvoice, error) {
	var i lockedInvoice
	var archived *time.Time
	var status string
	err := tx.QueryRow(ctx, `SELECT i.id,i.customer_id,i.line_items::text,i.tax_rate::text,i.deleted_at,coalesce(s.name,'') FROM invoices i LEFT JOIN statuses s ON s.id=i.status_id WHERE i.company_id=$1 AND i.id=$2 FOR UPDATE OF i`, companyID, id).Scan(&i.id, &i.customerID, &i.lineItems, &i.taxRate, &archived, &status)
	if err != nil {
		return i, err
	}
	if archived != nil {
		return i, ErrArchived
	}
	if strings.EqualFold(status, "void") {
		return i, ErrVoid
	}
	return i, nil
}
func lockCustomer(ctx context.Context, tx pgx.Tx, companyID, id int64) error {
	var archived *time.Time
	err := tx.QueryRow(ctx, `SELECT deleted_at FROM customers WHERE company_id=$1 AND id=$2 FOR UPDATE`, companyID, id).Scan(&archived)
	if err == nil && archived != nil {
		return errors.New("archived customer")
	}
	return err
}
func invoiceTotal(encoded, tax string) (int64, error) {
	items, err := services.DecodeLineItems(encoded)
	if err != nil {
		return 0, err
	}
	totals, err := services.CalculateTotals(items, tax)
	if err != nil {
		return 0, err
	}
	if totals.Total.MinorUnits() < 0 {
		return 0, errors.New("negative invoice total")
	}
	return totals.Total.MinorUnits(), nil
}

const creditAvailableSQL = `SELECT c.id,c.source_date, available.amount_cents FROM customer_credits c CROSS JOIN LATERAL (SELECT c.original_amount_cents-coalesce((SELECT sum(a.amount_cents) FROM credit_applications a WHERE a.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)),0)-coalesce((SELECT sum(ra.amount_cents) FROM credit_refund_allocations ra JOIN credit_refunds f ON f.id=ra.refund_id WHERE ra.credit_id=c.id AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_refund' AND r.operation_id=f.id)),0) amount_cents) available WHERE c.company_id=$1 AND c.customer_id=$2 AND NOT EXISTS(SELECT 1 FROM settlement_reversals source_reversal WHERE source_reversal.operation_type='payment' AND source_reversal.operation_id=c.source_payment_id)`

func activeSettled(ctx context.Context, tx pgx.Tx, invoiceID int64) (int64, error) {
	var n int64
	err := tx.QueryRow(ctx, `SELECT coalesce((SELECT sum(a.amount_cents) FROM payment_invoice_allocations a JOIN invoice_payments p ON p.id=a.payment_id WHERE a.invoice_id=$1 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=p.id)),0)+coalesce((SELECT sum(a.amount_cents) FROM credit_applications a WHERE a.invoice_id=$1 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='credit_application' AND r.operation_id=a.id)),0)`, invoiceID).Scan(&n)
	return n, err
}
func reproject(ctx context.Context, tx pgx.Tx, a Actor, invoiceID int64, action string, id uuid.UUID) error {
	inv, err := lockInvoice(ctx, tx, a.CompanyID, invoiceID)
	if err != nil {
		return err
	}
	total, err := invoiceTotal(inv.lineItems, inv.taxRate)
	if err != nil {
		return err
	}
	settled, err := activeSettled(ctx, tx, invoiceID)
	if err != nil {
		return err
	}
	state, _ := StateFor(total, settled)
	return projectAndLog(ctx, tx, a, invoiceID, state, action, id)
}
func projectAndLog(ctx context.Context, tx pgx.Tx, a Actor, invoiceID int64, state State, action string, id uuid.UUID) error {
	if _, err := tx.Exec(ctx, `SELECT settlement_set_invoice_state($1,$2)`, invoiceID, state); err != nil {
		return err
	}
	return logActivity(ctx, tx, a, "invoice", invoiceID, action, id)
}
func logActivity(ctx context.Context, tx pgx.Tx, a Actor, typ string, objectID int64, action string, id uuid.UUID) error {
	metadata, _ := json.Marshal(map[string]string{"settlement_operation_id": id.String()})
	_, err := tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,$3,$4,$5,$6)`, a.CompanyID, a.ID, action, typ, objectID, metadata)
	return err
}
func authorize(a Actor) error {
	if a.ID <= 0 || a.CompanyID <= 0 || (a.Role != "admin" && a.Role != "dispatcher") {
		return ErrForbidden
	}
	return nil
}
func validMethod(m PaymentMethod) bool {
	return m == Cash || m == Check || m == CreditCard || m == Transfer || m == Other
}

func (s *Service) transact(ctx context.Context, a Actor, operation string, op Operation, request any, fn func(pgx.Tx, time.Time) (Result, error)) (Result, error) {
	if strings.TrimSpace(op.Key) == "" {
		return Result{}, errors.New("idempotency key is required")
	}
	canonical, err := semanticJSON(request)
	if err != nil {
		return Result{}, err
	}
	sum := sha256.Sum256(canonical)
	fingerprint := hex.EncodeToString(sum[:])
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return Result{}, err
	}
	defer tx.Rollback(ctx)
	// Serialize contenders before checking/inserting the key. Unlike a bare unique
	// constraint, the loser can replay the committed result instead of surfacing 23505.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, fmt.Sprintf("%d:%s:%s", a.CompanyID, operation, op.Key)); err != nil {
		return Result{}, err
	}
	var existing string
	var resultID *uuid.UUID
	var resultJSON []byte
	err = tx.QueryRow(ctx, `SELECT request_fingerprint,result_id,result_json FROM settlement_idempotency WHERE company_id=$1 AND operation=$2 AND idempotency_key=$3 FOR UPDATE`, a.CompanyID, operation, op.Key).Scan(&existing, &resultID, &resultJSON)
	if err == nil {
		if existing != fingerprint {
			return Result{}, ErrIdempotencyConflict
		}
		if resultID == nil {
			return Result{}, errors.New("idempotent operation incomplete")
		}
		var replay Result
		if err := json.Unmarshal(resultJSON, &replay); err != nil {
			return Result{}, err
		}
		return replay, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO settlement_idempotency(company_id,operation,idempotency_key,request_fingerprint) VALUES($1,$2,$3,$4)`, a.CompanyID, operation, op.Key, fingerprint); err != nil {
		return Result{}, err
	}
	var zone string
	if err = tx.QueryRow(ctx, `SELECT timezone FROM company_settings WHERE company_id=$1`, a.CompanyID).Scan(&zone); err != nil {
		return Result{}, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return Result{}, fmt.Errorf("invalid company timezone: %w", err)
	}
	result, err := fn(tx, s.now().In(loc))
	if err != nil {
		return Result{}, err
	}
	resultJSON, err = json.Marshal(result)
	if err != nil {
		return Result{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE settlement_idempotency SET result_id=$1,result_json=$2 WHERE company_id=$3 AND operation=$4 AND idempotency_key=$5`, result.ID, resultJSON, a.CompanyID, operation, op.Key); err != nil {
		return Result{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return result, nil
}

func semanticJSON(request any) ([]byte, error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	delete(fields, "Operation")
	return json.Marshal(fields)
}

func parseRequestDate(value Date, location *time.Location) (time.Time, error) {
	if strings.TrimSpace(string(value)) == "" {
		return time.Time{}, errors.New("date is required")
	}
	parsed, err := time.ParseInLocation("2006-01-02", string(value), location)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid calendar date: %w", err)
	}
	return parsed, nil
}
