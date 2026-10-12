package authorization

import (
	"context"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
)

// ReviewLegacyNumber records an explicit supplied identity, never a guessed
// historical prefix or regenerated PDF. Other legacy records remain untouched.
func (s *Service) ReviewLegacyNumber(ctx context.Context, a Actor, revision int64, kind string, id, number, next int64, label string) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("configuration.documents.manage", Configuration) {
			return ErrForbidden
		}
		if (kind != "invoice" && kind != "estimate") || id < 1 || number < 1 || next <= number || next == math.MaxInt64 || strings.TrimSpace(label) == "" || !validNumberText(label) {
			return ErrConfigurationInput
		}
		var current int64
		if err := tx.QueryRow(ctx, `SELECT configuration_revision FROM company_settings WHERE company_id=$1 FOR UPDATE`, p.CompanyID).Scan(&current); err != nil {
			return err
		}
		if current != revision {
			return ErrConflict
		}
		var oldLabel string
		var oldNumber int64
		table := "invoices"
		numberColumn := "invoice_number"
		nextColumn := "next_invoice_number"
		if kind == "estimate" {
			table = "estimates"
			numberColumn = "coalesce(document_number,0)"
			nextColumn = "next_estimate_number"
		}
		var previousNext int64
		if err := tx.QueryRow(ctx, `SELECT `+nextColumn+` FROM company_settings WHERE company_id=$1`, p.CompanyID).Scan(&previousNext); err != nil {
			return err
		}
		if next < previousNext {
			return ErrConfigurationInput
		}
		if err := tx.QueryRow(ctx, `SELECT number_label,`+numberColumn+` FROM `+table+` WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, p.CompanyID).Scan(&oldLabel, &oldNumber); err != nil {
			return ErrNotFound
		}
		if oldLabel != "" || (kind == "invoice" && number != oldNumber) {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `INSERT INTO issued_document_numbers VALUES($1,$2,$3,$4)`, p.CompanyID, kind, number, label); err != nil {
			return err
		}
		query := `UPDATE invoices SET number_label=$2 WHERE id=$1`
		args := []any{id, label}
		if kind == "estimate" {
			query = `UPDATE estimates SET number_label=$2,document_number=$3 WHERE id=$1`
			args = append(args, number)
		}
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE company_settings SET `+nextColumn+`=$2,updated_at=clock_timestamp() WHERE company_id=$1`, p.CompanyID, next); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "configuration.documents.manage", Configuration, kind, id, map[string]any{"legacy_number_review": true, "number": number, "label": label, "previous_next_number": previousNext, "reviewed_next_number": next})
	})
}
