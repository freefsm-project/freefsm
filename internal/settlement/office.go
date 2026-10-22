package settlement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RecordInvoicePaymentWithCredit is the office-only, independently granted
// receipt action. The bounded field action never calls this entry point.
func (s *Service) RecordInvoicePaymentWithCredit(ctx context.Context, a authorization.Actor, req RecordPaymentRequest) (Result, error) {
	if req.InvoiceID <= 0 {
		return Result{}, ErrInvalidAmount
	}
	return s.recordOfficePayment(ctx, a, 0, req)
}

// RecordCustomerPayment records money for one fixed Customer with at most one
// selected Invoice. Zero InvoiceID explicitly means an Unapplied Payment.
func (s *Service) RecordCustomerPayment(ctx context.Context, a authorization.Actor, customerID int64, req RecordPaymentRequest) (Result, error) {
	if customerID <= 0 || req.InvoiceID < 0 {
		return Result{}, ErrInvalidAmount
	}
	return s.recordOfficePayment(ctx, a, customerID, req)
}

func (s *Service) recordOfficePayment(ctx context.Context, a authorization.Actor, customerID int64, req RecordPaymentRequest) (Result, error) {
	var result Result
	if a.Channel == "api" {
		return result, authorization.ErrForbidden
	}
	if id, err := uuid.Parse(req.Operation.Key); err != nil || id == uuid.Nil || req.AmountCents <= 0 || !validMethod(req.Method) {
		return result, ErrInvalidAmount
	}
	err := authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		if !p.Allows("payment.credit", authorization.All) || !p.Allows("customer.financial.view", authorization.All) {
			return authorization.ErrForbidden
		}
		// Common admission binds exact integer intent, actor and client before any
		// effect. Retrying this action cannot collide with a bounded Payment.
		var intent any = req // Retain the installed Invoice-route admission shape.
		if customerID != 0 {
			intent = struct {
				CustomerID int64
				Receipt    RecordPaymentRequest
			}{customerID, req}
		}
		if err := authorization.AdmitOperation(ctx, tx, a, req.Operation.Key, "payment.credit", intent); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT result_json FROM settlement_idempotency WHERE company_id=$1 AND operation='record_payment' AND idempotency_key=$2`, p.CompanyID, req.Operation.Key).Scan(&raw)
		if err == nil {
			return json.Unmarshal(raw, &result)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var inv lockedInvoice
		if req.InvoiceID != 0 {
			if _, err := authorization.CheckParentTransaction(ctx, tx, p, "invoice", req.InvoiceID, ""); err != nil {
				return err
			}
			inv, err = lockInvoice(ctx, tx, p.CompanyID, req.InvoiceID)
			if err != nil {
				return err
			}
			if customerID != 0 && customerID != inv.customerID {
				return authorization.ErrNotFound
			}
			customerID = inv.customerID
		}
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "customer", customerID, "payment.credit"); err != nil {
			return err
		}
		if err := lockCustomer(ctx, tx, p.CompanyID, customerID); err != nil {
			return err
		}
		var category, zone string
		if req.InvoiceID != 0 {
			if err := tx.QueryRow(ctx, `SELECT coalesce(s.category_key,'') FROM invoices i LEFT JOIN statuses s ON s.id=i.status_id WHERE i.id=$1`, inv.id).Scan(&category); err != nil {
				return err
			}
			if category != "invoice:invoiced" && category != "invoice:sent" {
				return ErrVoid
			}
			total, err := invoiceTotal(inv.lineItems, inv.taxRate, inv.financial)
			if err != nil {
				return err
			}
			settled, err := activeSettled(ctx, tx, inv.id)
			if err != nil {
				return err
			}
			if total <= settled {
				return ErrInvalidAmount
			}
		}
		if err := tx.QueryRow(ctx, `SELECT timezone FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&zone); err != nil {
			return err
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		actor := Actor{ID: p.UserID, CompanyID: p.CompanyID}
		targetType, targetID := "invoice", req.InvoiceID
		if req.InvoiceID != 0 {
			result, err = writePayment(ctx, tx, actor, req, s.now().In(loc), false)
		} else {
			date, e := parseRequestDate(req.ReceivedDate, loc)
			if e != nil {
				return e
			}
			if e = ValidateEffectiveDate(date, s.now().In(loc), time.Time{}); e != nil {
				return e
			}
			result.ID, err = writeReceipt(ctx, tx, actor, req, customerID, date, 0, req.AmountCents)
			result.CreditCents = req.AmountCents
			targetType, targetID = "customer", customerID
			if err == nil {
				err = logActivity(ctx, tx, actor, "customer", customerID, "payment_recorded", result.ID)
			}
		}
		if err != nil {
			return err
		}
		if err := protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: p.UserID, RoleID: p.RoleID, Action: "payment.credit", Scope: string(authorization.All), TargetType: targetType, TargetID: targetID, Channel: a.Channel, SessionHash: a.SessionHash, Detail: map[string]any{"operation_id": req.Operation.Key, "payment_id": result.ID.String()}}); err != nil {
			return err
		}
		canonical, err := semanticJSON(req)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(canonical)
		raw, err = json.Marshal(result)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO settlement_idempotency(company_id,operation,idempotency_key,request_fingerprint,result_id,result_json) VALUES($1,'record_payment',$2,$3,$4,$5)`, p.CompanyID, req.Operation.Key, hex.EncodeToString(sum[:]), result.ID, raw)
		return err
	})
	return result, err
}
