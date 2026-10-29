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
	return s.createBoundDocument(ctx, tx, a, p, kind, in, prepare, 0)
}

// obligation is server-owned and usable only for an exact, pending Lease charge.
// Ordinary callers always pass zero and retain all independent Invoice grants.
func (s *Service) createBoundDocument(ctx context.Context, tx pgx.Tx, a Actor, p Principal, kind string, in DocumentCreation, prepare func(string) (string, error), obligation int64) (int64, error) {
	return s.createApprovedDocument(ctx, tx, a, p, kind, in, prepare, obligation, nil)
}

// An obligation capability is constructed only after locked source validation.
// Workers have no product identity; explicit replacements additionally require
// current product authority and an exact reviewed predecessor.
type leaseGenerationCapability struct{ obligation, company, predecessor int64 }

func (s *Service) createApprovedDocument(ctx context.Context, tx pgx.Tx, a Actor, p Principal, kind string, in DocumentCreation, prepare func(string) (string, error), obligation int64, capability *leaseGenerationCapability) (int64, error) {
	var id int64
	err := func() error {
		if kind != "invoice" && kind != "estimate" {
			return ErrForbidden
		}
		scope := All
		if capability != nil && (obligation == 0 || capability.obligation != obligation || capability.company != p.CompanyID || (capability.predecessor == 0 && a.UserID != 0) || (capability.predecessor != 0 && (!p.Allows("lease.manage", All) || !p.Allows("lease.billing.manage", All)))) {
			return ErrForbidden
		}
		if obligation != 0 {
			if kind != "invoice" || a.Channel == "api" || (capability == nil && (!p.Allows("lease.manage", All) || !p.Allows("lease.billing.manage", All))) || in.JobID != 0 || in.EstimateID != 0 || in.Number != nil {
				return ErrForbidden
			}
			var valid bool
			predecessor := int64(0)
			if capability != nil {
				predecessor = capability.predecessor
			}
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_billing_obligation_current o JOIN leases l ON l.id=o.lease_id WHERE o.id=$1 AND l.company_id=$2 AND l.customer_id=$3 AND l.deleted_at IS NULL AND o.financial=$4 AND o.line_items=$5::jsonb AND o.due_on=$6 AND o.due_on<=lease_dependency_today(l.company_id)+30 AND (($7::bigint=0 AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations g WHERE g.obligation_id=o.id)) OR EXISTS(SELECT 1 FROM lease_invoice_generations g JOIN invoices i ON i.id=g.invoice_id JOIN statuses s ON s.id=i.status_id WHERE g.obligation_id=o.id AND g.invoice_id=$7 AND s.category_key='invoice:void' AND i.deleted_at IS NULL AND NOT EXISTS(SELECT 1 FROM lease_invoice_generations n WHERE n.predecessor_invoice_id=g.invoice_id))))`, obligation, p.CompanyID, in.CustomerID, in.Financial, in.LineItems, in.DueDate, predecessor).Scan(&valid); err != nil {
				return err
			}
			if !valid {
				return ErrConflict
			}
		} else if !p.Allows(kind+".create", All) {
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
		if obligation == 0 && !p.Allows("customer.view", All) && !(assigned && p.Allows("customer.view", Context)) {
			return ErrNotFound
		}
		if obligation == 0 && !p.Allows("pricing.view", All) && !(assigned && p.Allows("pricing.view", Assigned)) {
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
		if capability == nil {
			if err := s.CheckItemSelections(ctx, a, tx, kind, 0, in.LineItems); err != nil {
				return err
			}
		} else {
			var lines []struct {
				ItemID int64 `json:"item_id"`
			}
			if err := json.Unmarshal([]byte(in.LineItems), &lines); err != nil {
				return err
			}
			for _, line := range lines {
				if line.ItemID != 0 {
					return ErrForbidden
				}
			}
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
			if obligation != 0 && snapshot.Input.TaxExempt != exempt {
				return ErrConflict
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
		action := kind + ".create"
		if obligation != 0 {
			action = "lease.invoice.generate"
			detail["obligation_id"] = obligation
		}
		if capability != nil && a.UserID == 0 {
			raw, err := json.Marshal(detail)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO authorization_audit(actor_kind,action,target_type,target_id,channel,detail) VALUES('lease-billing-worker','lease.invoice.generate','invoice',$1,'worker',$2)`, id, raw)
			return err
		}
		return audit(ctx, tx, a, p, action, scope, kind, id, detail)
	}()
	return id, err
}
