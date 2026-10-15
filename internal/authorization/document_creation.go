package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// DocumentCreation contains resolved, explicitly reviewed content. Defaults are
// not reapplied here; current authority, selected inputs and numbering are.
type DocumentCreation struct {
	Financial                                             string
	CustomerID, JobID, EstimateID                         int64
	Title, Notes, Terms, TaxRate, LineItems, CustomFields string
	InvoiceDate, DueDate                                  time.Time
	ValidThrough                                          *time.Time
	Number                                                *int64
}

func (s *Service) CreateDocument(ctx context.Context, a Actor, kind string, in DocumentCreation, prepare func(string) (string, error)) (int64, error) {
	var id int64
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		var err error
		id, err = s.createDocument(ctx, tx, a, p, kind, in, prepare)
		return err
	})
	return id, err
}

// CreateDocumentInTransaction joins the caller's details transaction. It never
// commits independently: fields, selections, numbering and audits share its fate.
func (s *Service) CreateDocumentInTransaction(ctx context.Context, tx pgx.Tx, a Actor, kind string, in DocumentCreation, prepare func(string) (string, error)) (int64, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return 0, err
	}
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return 0, err
	}
	return s.createDocument(ctx, tx, a, p, kind, in, prepare)
}

func (s *Service) createDocument(ctx context.Context, tx pgx.Tx, a Actor, p Principal, kind string, in DocumentCreation, prepare func(string) (string, error)) (int64, error) {
	var id int64
	err := func() error {
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
		if in.JobID > 0 {
			var job int64
			if err := tx.QueryRow(ctx, `SELECT id FROM jobs WHERE id=$1 AND company_id=$2 AND customer_id=$3 AND deleted_at IS NULL FOR SHARE`, in.JobID, p.CompanyID, in.CustomerID).Scan(&job); err == pgx.ErrNoRows {
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
		// Only explicit server-owned copy context exempts retained occurrences.
		// A submitted Job binding alone is not proof of a copied snapshot.
		if err := s.CheckItemSelections(ctx, a, tx, kind, 0, in.LineItems); err != nil {
			return err
		}
		if exempt && in.Financial == "" {
			in.TaxRate = "0"
		}
		state, err := prepare(in.TaxRate)
		if err != nil {
			return err
		}
		if in.Financial != "" {
			snapshot, err := commercial.Read(in.Financial)
			if err != nil {
				return err
			}
			snapshot.Input.TaxExempt = exempt
			in.Financial, err = commercial.Accept(snapshot.Input)
			if err != nil {
				return fmt.Errorf("%w: %v", ErrCommercialInput, err)
			}
			snapshot, err = commercial.Read(in.Financial)
			if err != nil {
				return err
			}
			state = "unpaid"
			if snapshot.Result.TotalCents == 0 {
				state = "paid"
			}
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
			err = tx.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,estimate_id,status_id,invoice_number,title,notes,terms,invoice_date,due_date,tax_rate,line_items,custom_fields,settlement_state,financial) VALUES($1,$2,NULLIF($3,0),NULLIF($4,0),$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14::jsonb,$15,$16) RETURNING id`, p.CompanyID, in.CustomerID, in.JobID, in.EstimateID, status, number, in.Title, in.Notes, in.Terms, in.InvoiceDate, in.DueDate, in.TaxRate, in.LineItems, in.CustomFields, state, in.Financial).Scan(&id)
		} else {
			err = tx.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,notes,terms,valid_through,tax_rate,line_items,custom_fields,financial) VALUES($1,$2,NULLIF($3,0),$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb,$12) RETURNING id`, p.CompanyID, in.CustomerID, in.JobID, status, in.Title, in.Notes, in.Terms, in.ValidThrough, in.TaxRate, in.LineItems, in.CustomFields, in.Financial).Scan(&id)
		}
		if err != nil {
			return err
		}
		var label string
		if err := tx.QueryRow(ctx, `SELECT number_label FROM `+kind+`s WHERE id=$1`, id).Scan(&label); err != nil {
			return err
		}
		detail := map[string]any{"number": label, "customer_id": in.CustomerID, "job_id": in.JobID}
		if in.Financial != "" {
			detail["financial"] = json.RawMessage(in.Financial)
		}
		return audit(ctx, tx, a, p, kind+".create", scope, kind, id, detail)
	}()
	return id, err
}
