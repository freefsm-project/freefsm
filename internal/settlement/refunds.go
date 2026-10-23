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

// refundCommand admits office refunds and their explicit compensations under
// the same authority/commercial fence as Payments and Applications. The existing
// immutable writers remain the only source of financial effects.
func (s *Service) refundCommand(ctx context.Context, a authorization.Actor, permission, operation string, op Operation, targetType string, targetID int64, request any, write func(pgx.Tx, Actor, time.Time) (Result, error)) (Result, error) {
	var out Result
	if a.Channel == "api" {
		return out, authorization.ErrForbidden
	}
	if id, err := uuid.Parse(op.Key); err != nil || id == uuid.Nil {
		return out, ErrInvalidAmount
	}
	canonical, err := semanticJSON(request)
	if err != nil {
		return out, err
	}
	sum := sha256.Sum256(canonical)
	fingerprint := hex.EncodeToString(sum[:])
	err = authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, targetType, targetID, ""); err != nil {
			return err
		}
		if !p.Allows(permission, authorization.All) || !p.Allows("customer.financial.view", authorization.All) {
			return authorization.ErrForbidden
		}
		if permission == "credit.refund" {
			if !p.Allows("credit.view", authorization.All) {
				return authorization.ErrForbidden
			}
		} else if !p.Allows("settlement.view", authorization.All) {
			return authorization.ErrForbidden
		}
		customerID := targetID
		if targetType == "invoice" {
			if err := tx.QueryRow(ctx, `SELECT customer_id FROM invoices WHERE id=$1`, targetID).Scan(&customerID); err != nil {
				return err
			}
			if _, err := authorization.CheckParentTransaction(ctx, tx, p, "customer", customerID, ""); err != nil {
				return err
			}
		}
		if err := authorization.AdmitOperation(ctx, tx, a, op.Key, permission, request); err != nil {
			return err
		}
		var stored string
		var raw []byte
		var owner int64
		err := tx.QueryRow(ctx, `SELECT i.request_fingerprint,i.result_json,coalesce(f.actor_id,r.actor_id,0) FROM settlement_idempotency i LEFT JOIN credit_refunds f ON i.operation='refund_credit' AND f.id=i.result_id LEFT JOIN settlement_reversals r ON i.operation IN ('reverse_payment','reverse_credit_refund') AND r.id=i.result_id WHERE i.company_id=$1 AND i.operation=$2 AND i.idempotency_key=$3`, p.CompanyID, operation, op.Key).Scan(&stored, &raw, &owner)
		if err == nil {
			if stored != fingerprint || owner != p.UserID {
				return ErrIdempotencyConflict
			}
			return json.Unmarshal(raw, &out)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err := lockCustomer(ctx, tx, p.CompanyID, customerID); err != nil {
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
		out, err = write(tx, Actor{ID: p.UserID, CompanyID: p.CompanyID}, s.now().In(loc))
		if err != nil {
			return err
		}
		if err := protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: p.UserID, RoleID: p.RoleID, Action: permission, Scope: string(authorization.All), TargetType: targetType, TargetID: targetID, Channel: a.Channel, SessionHash: a.SessionHash, Detail: map[string]any{"operation_id": op.Key, "settlement_id": out.ID}}); err != nil {
			return err
		}
		raw, err = json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO settlement_idempotency(company_id,operation,idempotency_key,request_fingerprint,result_id,result_json) VALUES($1,$2,$3,$4,$5,$6)`, p.CompanyID, operation, op.Key, fingerprint, out.ID, raw)
		return err
	})
	return out, err
}
