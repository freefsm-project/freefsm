package authorization

import (
	"context"
	"github.com/jackc/pgx/v5"
)

func (s *Service) JobPlanningFacets(ctx context.Context, tx pgx.Tx, a Actor, id int64, changes map[string]bool) error {
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	for permission, changed := range changes {
		if !changed {
			continue
		}
		if id == 0 {
			if !p.Allows(permission, All) {
				return ErrForbidden
			}
		} else if _, err := parentAccess(ctx, tx, p, "job", id, permission, true); err != nil {
			return err
		}
	}
	return nil
}

func JobStatusDependencies(ctx context.Context, tx pgx.Tx, companyID, id int64, category string) error {
	if category == "job:completed" || category == "job:canceled" {
		return nil
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT c.deleted_at IS NULL
 AND (j.location_id IS NULL OR EXISTS(SELECT 1 FROM locations l WHERE l.id=j.location_id AND l.company_id=j.company_id AND l.object_type='customer' AND l.object_id=j.customer_id AND l.deleted_at IS NULL))
 AND (j.asset_id IS NULL OR EXISTS(SELECT 1 FROM assets a WHERE a.id=j.asset_id AND a.company_id=j.company_id AND a.customer_id=j.customer_id AND a.deleted_at IS NULL))
 AND NOT EXISTS(SELECT 1 FROM job_contacts jc JOIN customer_contacts cc ON cc.id=jc.contact_id WHERE jc.job_id=j.id AND (cc.deleted_at IS NOT NULL OR cc.customer_id<>j.customer_id OR cc.company_id<>j.company_id))
 FROM jobs j JOIN customers c ON c.id=j.customer_id AND c.company_id=j.company_id WHERE j.id=$1 AND j.company_id=$2 FOR UPDATE OF c`, id, companyID).Scan(&valid)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !valid {
		return ErrConflict
	}
	return nil
}

// JobInputs validates independently selected masters inside the committing
// details transaction. Retained historical links do not become new selections.
func (s *Service) JobInputs(ctx context.Context, tx pgx.Tx, a Actor, id, customerID int64, replacing bool, links map[string]int64) error {
	p, err := principal(ctx, tx, a, false)
	if err != nil {
		return err
	}
	if a.Channel == "api" {
		return ErrForbidden
	}
	var oldCustomer int64
	old := map[string]int64{}
	if id > 0 {
		var project, location, contact, asset int64
		if err := tx.QueryRow(ctx, `SELECT customer_id,coalesce(project_id,0),coalesce(location_id,0),coalesce(customer_contact_id,0),coalesce(asset_id,0) FROM jobs WHERE id=$1 AND company_id=$2 FOR UPDATE`, id, p.CompanyID).Scan(&oldCustomer, &project, &location, &contact, &asset); err != nil {
			return err
		}
		old = map[string]int64{"project": project, "location": location, "contact": contact, "asset": asset}
	}
	if id == 0 || customerID != oldCustomer {
		if !p.Allows("customer.view", All) {
			return ErrNotFound
		}
	}
	active, err := customerLifecycleState(ctx, tx, p, customerID, true)
	if err != nil {
		return err
	}
	if !active {
		return ErrConflict
	}
	if id > 0 && customerID != oldCustomer {
		if !replacing {
			return ErrConflict
		}
		// The details boundary already authorizes planning Edit. Clearing
		// incompatible links is its inseparable effect, not extra discretion.
		var blocked bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoices i LEFT JOIN statuses s ON s.id=i.status_id WHERE i.job_id=$1 AND i.conversion_hidden_at IS NULL AND (coalesce(s.category_key,'')<>'invoice:draft' OR i.deleted_at IS NOT NULL OR invoice_has_active_settlement(i.id))) OR EXISTS(SELECT 1 FROM job_customer_blockers WHERE job_id=$1)`, id).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return ErrConflict
		}
		return nil
	}
	for _, kind := range []string{"project", "location", "contact", "asset"} {
		selected := links[kind]
		if selected == old[kind] {
			continue
		}
		if !p.Allows("job.relations.edit", All) {
			return ErrForbidden
		}
		if selected == 0 {
			continue
		}
		if !p.Allows(kind+".view", All) {
			return ErrNotFound
		}
		table, owner := kind+"s", "customer_id"
		if kind == "contact" {
			table = "customer_contacts"
		}
		if kind == "location" {
			owner = "object_id"
		}
		extra := ""
		if kind == "location" {
			extra = " AND object_type='customer'"
		}
		var active bool
		err := tx.QueryRow(ctx, `SELECT deleted_at IS NULL FROM `+table+` WHERE id=$1 AND company_id=$2 AND `+owner+`=$3`+extra+` FOR UPDATE`, selected, p.CompanyID, customerID).Scan(&active)
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
