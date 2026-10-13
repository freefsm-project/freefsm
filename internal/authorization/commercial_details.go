package authorization

import (
	"bytes"
	"context"
	"encoding/json"
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
	for _, key := range []string{"title", "notes", "terms", "custom_fields", "line_items", "tax_rate", "invoice_date", "due_date", "valid_through", "customer_id", "job_id"} {
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
	if strings.TrimSpace(next.Title) == "" {
		return ErrConflict
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
		// These are inseparable planning effects, not invented document grants.
		if err := audit(ctx, tx, a, p, "job.edit", All, v.kind, v.id, map[string]any{"job_id": jobID, "before": map[string]any{"customer_id": before}, "after": map[string]any{"customer_id": after}, "reason": "job_customer_reassignment"}); err != nil {
			return err
		}
	}
	return nil
}
