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

type ApplicationPage struct {
	InvoiceID                         int64
	Today                             string
	CanApply, CanReverse, ShowHistory bool
	DueCents                          int64
	Sources                           []CreditSource
	Applications                      []CreditApplicationEntry
}

// ApplicationContext is a single current-authority projection. Credit View is
// sufficient for Apply's selected input; settlement-history View is independent.
func (s *Service) ApplicationContext(ctx context.Context, a authorization.Actor, invoiceID int64) (ApplicationPage, error) {
	out := ApplicationPage{InvoiceID: invoiceID}
	if a.Channel == "api" {
		return out, authorization.ErrForbidden
	}
	err := authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "invoice", invoiceID, ""); err != nil {
			return err
		}
		var customerID int64
		var active bool
		var category, lines, tax, financial, zone string
		if err := tx.QueryRow(ctx, `SELECT i.customer_id,i.deleted_at IS NULL AND c.deleted_at IS NULL,coalesce(st.category_key,''),i.line_items::text,i.tax_rate::text,i.financial,cs.timezone FROM invoices i JOIN customers c ON c.id=i.customer_id JOIN company_settings cs ON cs.company_id=i.company_id LEFT JOIN statuses st ON st.id=i.status_id WHERE i.id=$1`, invoiceID).Scan(&customerID, &active, &category, &lines, &tax, &financial, &zone); err != nil {
			return err
		}
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "customer", customerID, ""); err != nil {
			return err
		}
		out.ShowHistory = p.Allows("settlement.view", authorization.All)
		out.CanReverse = active && category != "invoice:void" && out.ShowHistory && p.Allows("credit.application.reverse", authorization.All)
		out.CanApply = active && (category == "invoice:invoiced" || category == "invoice:sent") && p.Allows("credit.apply", authorization.All) && p.Allows("credit.view", authorization.All) && p.Allows("customer.financial.view", authorization.All)
		if !out.ShowHistory && !out.CanApply {
			return authorization.ErrForbidden
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return err
		}
		out.Today = s.now().In(loc).Format("2006-01-02")
		if out.CanApply {
			total, err := invoiceTotal(lines, tax, financial)
			if err != nil {
				return err
			}
			settled, err := activeSettled(ctx, tx, invoiceID)
			if err != nil {
				return err
			}
			out.DueCents = max(total-settled, 0)
			out.CanApply = out.DueCents > 0
			rows, err := tx.Query(ctx, creditAvailableSQL+` AND available.amount_cents>0 ORDER BY c.source_date,c.id`, p.CompanyID, customerID)
			if err != nil {
				return err
			}
			for rows.Next() {
				var source CreditSource
				if err := rows.Scan(&source.ID, &source.SourceDate, &source.AvailableCents); err != nil {
					rows.Close()
					return err
				}
				out.Sources = append(out.Sources, source)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		if out.ShowHistory {
			rows, err := tx.Query(ctx, `SELECT a.id,a.credit_id,a.invoice_id,a.amount_cents,a.effective_date,r.id,r.effective_date,r.reason,coalesce(r.reference,''),coalesce(r.notes,'') FROM credit_applications a LEFT JOIN settlement_reversals r ON r.operation_type='credit_application' AND r.operation_id=a.id WHERE a.company_id=$1 AND a.invoice_id=$2 ORDER BY a.effective_date,a.id`, p.CompanyID, invoiceID)
			if err != nil {
				return err
			}
			out.Applications, err = scanApplications(rows)
			return err
		}
		return nil
	})
	return out, err
}

type ApplicationReceipt struct {
	OperationID string `json:"operation_id"`
	Outcome     string `json:"outcome"`
}

// Read-only recovery is actor-owned and reveals no Invoice/source/amount/history.
// A retained outcome never authorizes replay or restoration of a cached page.
func (s *Service) ApplicationReceipt(ctx context.Context, a authorization.Actor, key string) (ApplicationReceipt, error) {
	out := ApplicationReceipt{OperationID: key}
	if a.Channel == "api" {
		return out, authorization.ErrForbidden
	}
	if _, err := uuid.Parse(key); err != nil {
		return out, authorization.ErrNotFound
	}
	err := authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		var found bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM settlement_idempotency i LEFT JOIN credit_applications a ON i.operation='apply_credit' AND a.id=i.result_id LEFT JOIN settlement_reversals r ON i.operation='reverse_credit_application' AND r.id=i.result_id WHERE i.company_id=$1 AND i.idempotency_key=$2 AND coalesce(a.actor_id,r.actor_id)=$3)`, p.CompanyID, key, p.UserID).Scan(&found)
		if err != nil {
			return err
		}
		if !found {
			return authorization.ErrNotFound
		}
		out.Outcome = "accepted"
		return nil
	})
	return out, err
}

// applicationCommand owns office admission, not a second financial ledger. Its
// writers are also used by installed internal settlement callers. WithinActor
// holds the shared commercial/authority fence before Invoice, Customer and source
// row locks, and rechecks identity through commit.
func (s *Service) applicationCommand(ctx context.Context, a authorization.Actor, permission, operation string, op Operation, invoiceID int64, request any, write func(pgx.Tx, Actor, time.Time) (Result, error)) (Result, error) {
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
		// Resolve visibility before action denial, so hidden/foreign targets do
		// not disclose their existence through financial validation errors.
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "invoice", invoiceID, ""); err != nil {
			return err
		}
		if !p.Allows(permission, authorization.All) {
			return authorization.ErrForbidden
		}
		if permission == "credit.apply" {
			if !p.Allows("credit.view", authorization.All) || !p.Allows("customer.financial.view", authorization.All) {
				return authorization.ErrForbidden
			}
		} else if !p.Allows("settlement.view", authorization.All) {
			return authorization.ErrForbidden
		}
		var customerID int64
		if err := tx.QueryRow(ctx, `SELECT customer_id FROM invoices WHERE id=$1`, invoiceID).Scan(&customerID); err != nil {
			return err
		}
		if _, err := authorization.CheckParentTransaction(ctx, tx, p, "customer", customerID, ""); err != nil {
			return err
		}
		if err := authorization.AdmitOperation(ctx, tx, a, op.Key, permission, request); err != nil {
			return err
		}
		var raw []byte
		var stored string
		var owner int64
		// Installed receipts keep their identity/fingerprint. A legacy key may
		// only be replayed by its recorded actor, never claimed by another user.
		err := tx.QueryRow(ctx, `SELECT i.request_fingerprint,i.result_json,coalesce(a.actor_id,r.actor_id,0) FROM settlement_idempotency i LEFT JOIN credit_applications a ON i.operation='apply_credit' AND a.id=i.result_id LEFT JOIN settlement_reversals r ON i.operation='reverse_credit_application' AND r.id=i.result_id WHERE i.company_id=$1 AND i.operation=$2 AND i.idempotency_key=$3`, p.CompanyID, operation, op.Key).Scan(&stored, &raw, &owner)
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
		if err := protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: p.UserID, RoleID: p.RoleID, Action: permission, Scope: string(authorization.All), TargetType: "invoice", TargetID: invoiceID, Channel: a.Channel, SessionHash: a.SessionHash, Detail: map[string]any{"operation_id": op.Key, "settlement_id": out.ID}}); err != nil {
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
