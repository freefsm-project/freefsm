package authorization

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Original actors may recover minimal installed effect identities after a new
// sign-in, just as with installed Payment receipts. This is not client binding
// or replay authority: no revision, number label, or past client is inferred.
func installedConversionReceipt(ctx context.Context, tx pgx.Tx, p Principal, key string) (CommercialReceipt, error) {
	r := CommercialReceipt{OperationID: key}
	rows, err := tx.Query(ctx, `SELECT operation,actor_id,result FROM estimate_invoice_conversion_operations WHERE company_id=$1 AND idempotency_key=$2`, p.CompanyID, key)
	if err != nil {
		return r, err
	}
	var raw []byte
	var kind string
	count, owned := 0, false
	for rows.Next() {
		var actor int64
		if err = rows.Scan(&kind, &actor, &raw); err != nil {
			rows.Close()
			return r, err
		}
		count++
		owned = owned || actor == p.UserID
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return r, err
	}
	if !owned {
		return r, ErrNotFound
	}
	if count != 1 || len(raw) == 0 {
		return r, ErrConflict
	}
	var result struct {
		CycleID               string `json:"cycle_id"`
		EstimateID, InvoiceID int64
		Reverted              bool
	}
	if json.Unmarshal(raw, &result) != nil || result.CycleID == "" || result.EstimateID <= 0 || result.InvoiceID <= 0 || (kind == "revert") != result.Reverted {
		return r, ErrConflict
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM estimate_invoice_conversion_cycles WHERE company_id=$1 AND id::text=$2 AND estimate_id=$3 AND invoice_id=$4 AND CASE WHEN $5='revert' THEN reverted_by=$6 AND reverted_at IS NOT NULL ELSE converted_by=$6 END)`, p.CompanyID, result.CycleID, result.EstimateID, result.InvoiceID, kind, p.UserID).Scan(&exists); err != nil {
		return r, err
	}
	if !exists {
		return r, ErrConflict
	}
	r.Outcome = "accepted"
	r.CycleID = result.CycleID
	r.ID = result.InvoiceID
	r.DocumentType = "invoice"
	if result.Reverted {
		r.ID = result.EstimateID
		r.DocumentType = "estimate"
	}
	return r, nil
}
