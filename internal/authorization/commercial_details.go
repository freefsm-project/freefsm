package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Activity records changed fields, not a restorable historical document image.
func commercialChanges(before, after json.RawMessage) (json.RawMessage, json.RawMessage, error) {
	var old, next map[string]json.RawMessage
	if err := json.Unmarshal(before, &old); err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(after, &next); err != nil {
		return nil, nil, err
	}
	a, b := map[string]json.RawMessage{}, map[string]json.RawMessage{}
	for _, key := range []string{"title", "notes", "terms", "custom_fields", "line_items", "financial", "tax_rate", "invoice_date", "due_date", "valid_through", "customer_id", "job_id"} {
		if !bytes.Equal(old[key], next[key]) {
			a[key] = old[key]
			b[key] = next[key]
		}
	}
	left, err := json.Marshal(a)
	if err != nil {
		return nil, nil, err
	}
	right, err := json.Marshal(b)
	return left, right, err
}

// Check the committed shape as well as the requested action. Assigned content
// authority cannot move anchors, even between two currently assigned Jobs.
func commercialDetailsCheck(ctx context.Context, tx pgx.Tx, p Principal, kind string, before, after json.RawMessage) error {
	var old, next struct {
		ID         int64  `json:"id"`
		CustomerID int64  `json:"customer_id"`
		JobID      *int64 `json:"job_id"`
		LeaseID    *int64 `json:"lease_id"`
		Title      string `json:"title"`
	}
	if err := json.Unmarshal(before, &old); err != nil {
		return err
	}
	if err := json.Unmarshal(after, &next); err != nil {
		return err
	}
	var oldFields, newFields map[string]json.RawMessage
	if err := json.Unmarshal(before, &oldFields); err != nil {
		return err
	}
	if err := json.Unmarshal(after, &newFields); err != nil {
		return err
	}
	if !bytes.Equal(oldFields["financial"], newFields["financial"]) || !bytes.Equal(oldFields["line_items"], newFields["line_items"]) || !bytes.Equal(oldFields["tax_rate"], newFields["tax_rate"]) {
		if _, err := parentAccess(ctx, tx, p, kind, next.ID, "pricing.view", true); err != nil {
			return err
		}
	}
	if strings.TrimSpace(next.Title) == "" {
		return ErrConflict
	}
	if err := CommercialCustomerForWrite(ctx, tx, old.CustomerID); err != nil {
		return err
	}
	if err := CommercialCustomerForWrite(ctx, tx, next.CustomerID); err != nil {
		return err
	}
	job := func(id *int64) int64 {
		if id == nil {
			return 0
		}
		return *id
	}
	if old.CustomerID == next.CustomerID && job(old.JobID) == job(next.JobID) {
		return nil
	}
	if !p.Allows(kind+".edit", All) {
		return ErrForbidden
	}
	if old.LeaseID != nil && old.CustomerID != next.CustomerID {
		return ErrConflict
	}
	if old.CustomerID != next.CustomerID && !p.Allows("customer.view", All) {
		return ErrNotFound
	}
	var active bool
	err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM customers WHERE id=$1 AND company_id=$2 FOR SHARE`, next.CustomerID, p.CompanyID).Scan(&active)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !active {
		return ErrConflict
	}
	if next.JobID != nil {
		if !p.Allows("job.view", All) {
			assigned, err := assignedJobAccess(ctx, tx, p.UserID, *next.JobID, true)
			if err != nil {
				return err
			}
			if !assigned || !p.Allows("job.view", Assigned) {
				return ErrNotFound
			}
		}
		err = tx.QueryRow(ctx, `SELECT deleted_at IS NULL AND customer_id=$3 FROM jobs WHERE id=$1 AND company_id=$2 FOR SHARE`, *next.JobID, p.CompanyID, next.CustomerID).Scan(&active)
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !active {
			return ErrConflict
		}
	}
	return nil
}

func auditJobCommercialReassignment(ctx context.Context, tx pgx.Tx, a Actor, p Principal, jobID int64, before, after json.RawMessage) error {
	rows, err := tx.Query(ctx, `SELECT 'estimate',id FROM estimates WHERE job_id=$1 AND conversion_hidden_at IS NULL UNION ALL SELECT 'invoice',id FROM invoices WHERE job_id=$1 AND conversion_hidden_at IS NULL ORDER BY 1,2`, jobID)
	if err != nil {
		return err
	}
	type target struct {
		kind string
		id   int64
	}
	var targets []target
	for rows.Next() {
		var v target
		if err := rows.Scan(&v.kind, &v.id); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range targets {
		// A reviewed Customer replacement is a monetary anchor edit, including
		// inseparable linked-document effects. The Job review token includes all
		// linked commercial revisions; no metadata-only Job edit enters here.
		var oldFinancial string
		var lines, rate string
		var exempt bool
		if err := tx.QueryRow(ctx, `SELECT d.financial,c.tax_exempt,d.line_items::text,d.tax_rate FROM `+v.kind+`s d JOIN customers c ON c.id=d.customer_id WHERE d.id=$1 FOR SHARE OF c`, v.id).Scan(&oldFinancial, &exempt, &lines, &rate); err != nil {
			return err
		}
		var input commercial.Input
		if oldFinancial == "" {
			var err error
			input, err = commercial.CapturedInput(lines, rate, nil, nil)
			if err != nil {
				return ErrConflict
			}
		} else {
			snapshot, err := commercial.Read(oldFinancial)
			if err != nil {
				return err
			}
			input = snapshot.Input
		}
		input.TaxExempt = exempt
		newFinancial, err := commercial.Accept(input)
		if err != nil {
			return ErrConflict
		}
		if newFinancial != oldFinancial {
			if _, err := tx.Exec(ctx, `UPDATE `+v.kind+`s SET financial=$2 WHERE id=$1`, v.id, newFinancial); err != nil {
				return err
			}
			if v.kind == "invoice" {
				accepted, err := commercial.Read(newFinancial)
				if err != nil {
					return err
				}
				state := "unpaid"
				if accepted.Result.TotalCents == 0 {
					state = "paid"
				}
				if _, err := tx.Exec(ctx, `UPDATE invoices SET settlement_state=$2 WHERE id=$1`, v.id, state); err != nil {
					return err
				}
			}
		}
		// These are inseparable planning effects, not invented document grants.
		if err := auditOperationalEvent(ctx, tx, a, p, "job.edit", All, v.kind, v.id, "updated", map[string]any{"job_id": jobID, "before": map[string]any{"customer_id": before, "financial": oldFinancial, "line_items": json.RawMessage(lines), "tax_rate": rate}, "after": map[string]any{"customer_id": after, "financial": newFinancial, "line_items": json.RawMessage(lines), "tax_rate": rate}, "reason": "job_customer_reassignment"}); err != nil {
			return err
		}
	}
	return nil
}
