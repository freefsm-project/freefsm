package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DocumentCreation contains resolved, explicitly reviewed content. Defaults are
// not reapplied here; current authority, selected inputs and numbering are.
type DocumentCreation struct {
	CustomerID, JobID, EstimateID                         int64
	Title, Notes, Terms, TaxRate, LineItems, CustomFields string
	InvoiceDate, DueDate                                  time.Time
	ValidThrough                                          *time.Time
	Number                                                *int64
}

func (s *Service) CreateDocument(ctx context.Context, a Actor, kind string, in DocumentCreation, prepare func(string) (string, error)) (int64, error) {
	var id int64
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if kind != "invoice" && kind != "estimate" {
			return ErrForbidden
		}
		scope := All
		if !p.Allows(kind+".create", All) {
			if in.JobID < 1 || !p.Allows(kind+".create", Assigned) {
				return ErrForbidden
			}
			scope = Assigned
		}
		if strings.TrimSpace(in.Title) == "" {
			return fmt.Errorf("document title is required")
		}
		var exempt bool
		if err := tx.QueryRow(ctx, `SELECT tax_exempt FROM customers WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL FOR SHARE`, in.CustomerID, p.CompanyID).Scan(&exempt); err == pgx.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		assigned := false
		var captured []byte
		if in.JobID > 0 {
			if err := tx.QueryRow(ctx, `SELECT line_items FROM jobs WHERE id=$1 AND company_id=$2 AND customer_id=$3 AND deleted_at IS NULL FOR SHARE`, in.JobID, p.CompanyID, in.CustomerID).Scan(&captured); err == pgx.ErrNoRows {
				return ErrNotFound
			} else if err != nil {
				return err
			}
			var assignment int64
			err := tx.QueryRow(ctx, `SELECT user_id FROM job_assignments WHERE job_id=$1 AND user_id=$2 FOR SHARE`, in.JobID, p.UserID).Scan(&assignment)
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
			assigned = err == nil
			if !p.Allows("job.view", All) && !(assigned && p.Allows("job.view", Assigned)) {
				return ErrNotFound
			}
		}
		if scope == Assigned && !assigned {
			return ErrForbidden
		}
		if !p.Allows("customer.view", All) && !(assigned && p.Allows("customer.view", Context)) {
			return ErrNotFound
		}
		if !p.Allows("pricing.view", All) && !(assigned && p.Allows("pricing.view", Assigned)) {
			return ErrForbidden
		}
		if in.EstimateID > 0 {
			if !p.Allows("estimate.view", All) {
				return ErrForbidden
			}
			var source int64
			if err := tx.QueryRow(ctx, `SELECT id FROM estimates WHERE id=$1 AND company_id=$2 AND customer_id=$3 AND deleted_at IS NULL AND conversion_hidden_at IS NULL FOR SHARE`, in.EstimateID, p.CompanyID, in.CustomerID).Scan(&source); err == pgx.ErrNoRows {
				return ErrNotFound
			} else if err != nil {
				return err
			}
		}
		// A retained Job snapshot is not a new catalog selection. Other Item
		// references require independently authorized, active Pricebook inputs.
		type line struct {
			ItemID int64 `json:"item_id"`
		}
		var lines, jobLines []line
		if err := json.Unmarshal([]byte(in.LineItems), &lines); err != nil {
			return err
		}
		if len(captured) > 0 {
			if err := json.Unmarshal(captured, &jobLines); err != nil {
				return err
			}
		}
		retained := map[int64]bool{}
		for _, line := range jobLines {
			retained[line.ItemID] = true
		}
		for _, line := range lines {
			if line.ItemID == 0 || retained[line.ItemID] {
				continue
			}
			if line.ItemID < 0 || !p.Allows("item.view", All) || !p.Allows("pricebook.view", All) {
				return ErrForbidden
			}
			var item int64
			if err := tx.QueryRow(ctx, `SELECT id FROM items WHERE id=$1 AND company_id=$2 AND deleted_at IS NULL FOR SHARE`, line.ItemID, p.CompanyID).Scan(&item); err == pgx.ErrNoRows {
				return ErrNotFound
			} else if err != nil {
				return err
			}
		}
		if exempt {
			in.TaxRate = "0"
		}
		state, err := prepare(in.TaxRate)
		if err != nil {
			return err
		}
		var status int64
		if err := tx.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.company_id=$1 AND w.company_id=$1 AND w.object_type=$2 AND s.category_key=$3 AND s.is_category_default FOR SHARE OF s`, p.CompanyID, kind, kind+":draft").Scan(&status); err != nil {
			return err
		}
		var next int64
		if err := tx.QueryRow(ctx, `SELECT next_invoice_number FROM company_settings WHERE company_id=$1 FOR UPDATE`, p.CompanyID).Scan(&next); err != nil {
			return err
		}
		if kind == "invoice" {
			number := next
			if in.Number != nil {
				number = *in.Number
			}
			if number < next || number < 1 {
				return ErrConflict
			}
			err = tx.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,estimate_id,status_id,invoice_number,title,notes,terms,invoice_date,due_date,tax_rate,line_items,custom_fields,settlement_state) VALUES($1,$2,NULLIF($3,0),NULLIF($4,0),$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14::jsonb,$15) RETURNING id`, p.CompanyID, in.CustomerID, in.JobID, in.EstimateID, status, number, in.Title, in.Notes, in.Terms, in.InvoiceDate, in.DueDate, in.TaxRate, in.LineItems, in.CustomFields, state).Scan(&id)
		} else {
			err = tx.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,notes,terms,valid_through,tax_rate,line_items,custom_fields) VALUES($1,$2,NULLIF($3,0),$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb) RETURNING id`, p.CompanyID, in.CustomerID, in.JobID, status, in.Title, in.Notes, in.Terms, in.ValidThrough, in.TaxRate, in.LineItems, in.CustomFields).Scan(&id)
		}
		if err != nil {
			return err
		}
		var label string
		if err := tx.QueryRow(ctx, `SELECT number_label FROM `+kind+`s WHERE id=$1`, id).Scan(&label); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, kind+".create", scope, kind, id, map[string]any{"number": label, "customer_id": in.CustomerID, "job_id": in.JobID})
	})
	return id, err
}
