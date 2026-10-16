package settlement

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BoundedIntent preserves the original amount and selected Invoice. Presentation
// labels and current due are never used to normalize a staged intent.
type BoundedIntent struct {
	OperationID      string        `json:"operation_id"`
	JobID            int64         `json:"job_id,omitempty"`
	Amount           string        `json:"amount"`
	Method           PaymentMethod `json:"method"`
	Date             Date          `json:"date"`
	Reference        string        `json:"reference,omitempty"`
	Note             string        `json:"note,omitempty"`
	OtherDescription string        `json:"other_description,omitempty"`
	OccurredAt       *time.Time    `json:"occurred_at,omitempty"`
}

// PaymentReceipt is deliberately nonsensitive, even after original access loss.
type PaymentReceipt struct {
	OperationID string    `json:"operation_id"`
	PaymentID   string    `json:"payment_id,omitempty"`
	Outcome     string    `json:"outcome"`
	AcceptedAt  time.Time `json:"accepted_at"`
}

func paymentClient(a authorization.Actor) string {
	if a.Channel == "api" {
		return "api:" + a.SessionHash
	}
	return "web"
}

func (s *Service) PaymentReceipt(ctx context.Context, a authorization.Actor, id string) (PaymentReceipt, error) {
	var r PaymentReceipt
	if _, err := uuid.Parse(id); err != nil {
		return r, authorization.ErrNotFound
	}
	err := authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, _ authorization.Principal) error {
		var raw []byte
		// Fresh authentication of the original user permits read-only recovery
		// after restore/session replacement. Admission still binds replay to the
		// original client, and this projection contains no financial content.
		if err := tx.QueryRow(ctx, `SELECT receipt FROM bounded_payment_operations WHERE operation_id=$1 AND user_id=$2`, id, a.UserID).Scan(&raw); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return authorization.ErrNotFound
			}
			return err
		}
		return json.Unmarshal(raw, &r)
	})
	return r, err
}

var paymentAmountPattern = regexp.MustCompile(`^[0-9]+\.[0-9]{2}$`)

func (s *Service) AcceptBoundedPayment(ctx context.Context, a authorization.Actor, invoice int64, in BoundedIntent) (PaymentReceipt, error) {
	r := PaymentReceipt{OperationID: in.OperationID}
	if id, err := uuid.Parse(in.OperationID); err != nil || id == uuid.Nil {
		return r, ErrInvalidAmount
	}
	err := authorization.New(s.db).WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		if err := authorization.AdmitOperation(ctx, tx, a, in.OperationID, "payment.bounded", map[string]any{"invoice_id": invoice, "intent": in}); err != nil {
			return err
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT receipt FROM bounded_payment_operations WHERE operation_id=$1`, in.OperationID).Scan(&raw)
		if err == nil {
			return json.Unmarshal(raw, &r)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return err
		}
		effect := func() error {
			scope, err := authorization.CheckParentTransaction(ctx, sp, p, "invoice", invoice, "payment.record")
			if err != nil {
				return err
			}
			var job int64
			var category string
			if err := sp.QueryRow(ctx, `SELECT coalesce(i.job_id,0),coalesce(s.category_key,'') FROM invoices i LEFT JOIN statuses s ON s.id=i.status_id WHERE i.id=$1`, invoice).Scan(&job, &category); err != nil {
				return err
			}
			if a.Channel == "api" && (job == 0 || job != in.JobID) {
				return authorization.ErrNotFound
			}
			if category != "invoice:invoiced" && category != "invoice:sent" {
				return ErrVoid
			}
			if !paymentAmountPattern.MatchString(in.Amount) || !validMethod(in.Method) {
				return ErrInvalidAmount
			}
			cents, err := strconv.ParseInt(strings.ReplaceAll(in.Amount, ".", ""), 10, 64)
			if err != nil || cents <= 0 {
				return ErrInvalidAmount
			}
			if in.OccurredAt != nil && (in.OccurredAt.IsZero() || in.OccurredAt.After(s.now())) {
				return ErrInvalidAmount
			}
			var zone string
			if err := sp.QueryRow(ctx, `SELECT timezone FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&zone); err != nil {
				return err
			}
			loc, err := time.LoadLocation(zone)
			if err != nil {
				return err
			}
			date, err := parseRequestDate(in.Date, loc)
			if err != nil {
				return ErrInvalidAmount
			}
			if ValidateEffectiveDate(date, s.now().In(loc), time.Time{}) != nil {
				return ErrInvalidAmount
			}
			result, err := writePayment(ctx, sp, Actor{ID: p.UserID, CompanyID: p.CompanyID}, RecordPaymentRequest{Operation: Operation{Key: in.OperationID}, InvoiceID: invoice, AmountCents: cents, Method: in.Method, ReceivedDate: in.Date, Reference: in.Reference, Notes: in.Note, OtherDescription: in.OtherDescription}, s.now().In(loc), true)
			if err != nil {
				return err
			}
			r.PaymentID = result.ID.String()
			return protectedaudit.RecordProduct(ctx, sp, protectedaudit.ProductAction{ActorID: p.UserID, RoleID: p.RoleID, Action: "payment.record", Scope: string(scope), TargetType: "invoice", TargetID: invoice, Channel: a.Channel, SessionHash: a.SessionHash, Detail: map[string]any{"operation_id": in.OperationID, "payment_id": r.PaymentID, "occurred_at": in.OccurredAt}})
		}()
		if effect != nil {
			if err := sp.Rollback(ctx); err != nil {
				return err
			}
			r.PaymentID = ""
			switch {
			case errors.Is(effect, authorization.ErrNotFound), errors.Is(effect, pgx.ErrNoRows):
				r.Outcome = "not_found"
			case errors.Is(effect, authorization.ErrForbidden):
				r.Outcome = "forbidden"
			case errors.Is(effect, ErrVoid), errors.Is(effect, ErrArchived), errors.Is(effect, ErrHidden):
				r.Outcome = "invoice_ineligible"
			case errors.Is(effect, ErrInvalidAmount):
				r.Outcome = "invalid_payment"
			default:
				return effect
			}
		} else {
			if err := sp.Commit(ctx); err != nil {
				return err
			}
			r.Outcome = "accepted"
		}
		if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&r.AcceptedAt); err != nil {
			return err
		}
		raw, err = json.Marshal(r)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO bounded_payment_operations(operation_id,user_id,client_key,receipt) VALUES($1,$2,$3,$4)`, in.OperationID, a.UserID, paymentClient(a), raw)
		return err
	})
	return r, err
}
