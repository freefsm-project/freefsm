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
		if err := authorization.AdmitOperation(ctx, tx, a, req.Operation.Key, "payment.credit", req); err != nil {
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
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "invoice", req.InvoiceID, ""); err != nil {
			return err
		}
		inv, err := lockInvoice(ctx, tx, p.CompanyID, req.InvoiceID)
		if err != nil {
			return err
		}
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "customer", inv.customerID, "payment.credit"); err != nil {
			return err
		}
		if err := lockCustomer(ctx, tx, p.CompanyID, inv.customerID); err != nil {
			return err
		}
		var category, zone string
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
		if err := tx.QueryRow(ctx, `SELECT timezone FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&zone); err != nil {
			return err
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		result, err = writePayment(ctx, tx, Actor{ID: p.UserID, CompanyID: p.CompanyID}, req, s.now().In(loc), false)
		if err != nil {
			return err
		}
		if err := protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: p.UserID, RoleID: p.RoleID, Action: "payment.credit", Scope: string(authorization.All), TargetType: "invoice", TargetID: inv.id, Channel: a.Channel, SessionHash: a.SessionHash, Detail: map[string]any{"operation_id": req.Operation.Key, "payment_id": result.ID.String()}}); err != nil {
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
