package settlement

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type MethodChoice struct {
	ID                  PaymentMethod `json:"id"`
	Label               string        `json:"label"`
	RequiresDescription bool          `json:"requires_description"`
	Revision            string        `json:"-"`
}

type PaymentEntryContext struct {
	Methods  []MethodChoice
	Eligible bool
	DueCents int64
}

// PrepareBoundedPayment exposes only this Invoice's action context, never
// Customer Credit, source receipts or unrelated financial history.
func (s *Service) PrepareBoundedPayment(ctx context.Context, a authorization.Actor, invoice int64) (PaymentEntryContext, error) {
	var out PaymentEntryContext
	err := authorization.New(s.db).WithinParent(ctx, a, "invoice", invoice, "payment.record", func(tx pgx.Tx, p authorization.Principal) error {
		var err error
		out.Methods, err = methodChoices(ctx, tx, p.CompanyID)
		if err != nil {
			return err
		}
		var active bool
		var lines, tax, financial string
		if err := tx.QueryRow(ctx, `SELECT i.deleted_at IS NULL AND c.deleted_at IS NULL AND coalesce(s.category_key IN ('invoice:invoiced','invoice:sent'),false),i.line_items::text,i.tax_rate,i.financial FROM invoices i JOIN customers c ON c.id=i.customer_id LEFT JOIN statuses s ON s.id=i.status_id WHERE i.id=$1`, invoice).Scan(&active, &lines, &tax, &financial); err != nil {
			return err
		}
		if !active {
			return nil
		}
		total, err := invoiceTotal(lines, tax, financial)
		if err != nil {
			return err
		}
		paid, err := activeSettled(ctx, tx, invoice)
		if err != nil {
			return err
		}
		out.DueCents = max(total-paid, 0)
		out.Eligible = out.DueCents > 0
		return nil
	})
	return out, err
}

// Defaults describe the established identities; overrides change presentation only.
var methodDefaults = []MethodChoice{{ID: Cash, Label: "Cash"}, {ID: Check, Label: "Check"}, {ID: CreditCard, Label: "Card"}, {ID: Transfer, Label: "Bank Transfer"}, {ID: Other, Label: "Other", RequiresDescription: true}}
var ErrMethodLabel = errors.New("invalid Payment method label")

func methodChoices(ctx context.Context, tx pgx.Tx, company int64) ([]MethodChoice, error) {
	choices := append([]MethodChoice(nil), methodDefaults...)
	for i := range choices {
		choices[i].Revision = uuid.Nil.String()
	}
	rows, err := tx.Query(ctx, `SELECT method,label,revision::text FROM payment_method_labels WHERE company_id=$1`, company)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id PaymentMethod
		var label, revision string
		if err := rows.Scan(&id, &label, &revision); err != nil {
			return nil, err
		}
		for i := range choices {
			if choices[i].ID == id {
				choices[i].Label = label
				choices[i].Revision = revision
			}
		}
	}
	return choices, rows.Err()
}

func (s *Service) PaymentMethods(ctx context.Context, a authorization.Actor, invoice int64) ([]MethodChoice, error) {
	var choices []MethodChoice
	err := authorization.New(s.db).WithinParent(ctx, a, "invoice", invoice, "payment.record", func(tx pgx.Tx, p authorization.Principal) error {
		var err error
		choices, err = methodChoices(ctx, tx, p.CompanyID)
		return err
	})
	return choices, err
}

func (s *Service) ConfiguredPaymentMethods(ctx context.Context, a authorization.Actor) ([]MethodChoice, error) {
	var choices []MethodChoice
	err := authorization.New(s.db).WithinPermission(ctx, a, "configuration.company.view", authorization.Configuration, func(tx pgx.Tx, p authorization.Principal) error {
		var err error
		choices, err = methodChoices(ctx, tx, p.CompanyID)
		return err
	})
	return choices, err
}

func (s *Service) RenamePaymentMethod(ctx context.Context, a authorization.Actor, id PaymentMethod, label, expected string) error {
	return authorization.New(s.db).WithinPermission(ctx, a, "configuration.company.manage", authorization.Configuration, func(tx pgx.Tx, p authorization.Principal) error {
		label = strings.TrimSpace(label)
		if !validMethod(id) || label == "" || utf8.RuneCountInString(label) > 80 {
			return ErrMethodLabel
		}
		choices, err := methodChoices(ctx, tx, p.CompanyID)
		if err != nil {
			return err
		}
		var before string
		for _, choice := range choices {
			if choice.ID == id {
				if expected != choice.Revision {
					return authorization.ErrConflict
				}
				before = choice.Label
			} else if strings.EqualFold(label, choice.Label) {
				return ErrMethodLabel
			}
		}
		if label == before {
			return nil
		}
		if _, err := tx.Exec(ctx, `INSERT INTO payment_method_labels(company_id,method,label) VALUES($1,$2,$3) ON CONFLICT(company_id,method) DO UPDATE SET label=excluded.label,revision=gen_random_uuid()`, p.CompanyID, id, label); err != nil {
			return err
		}
		return protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: p.UserID, RoleID: p.RoleID, Action: "configuration.company.manage", Scope: string(authorization.Configuration), TargetType: "company", TargetID: p.CompanyID, Channel: a.Channel, SessionHash: a.SessionHash, Detail: map[string]any{"payment_method": id, "before": before, "after": label}})
	})
}
