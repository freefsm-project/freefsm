package authorization

import "context"

// MasterRestoreAccess is the current, read-only discovery check for the Project
// and Asset lifecycle commands. Those commands recheck authority and dependencies
// in their committing transactions; rendering a control never grants authority.
// Dependency checks return only eligibility, not hidden master labels or fields.
func (s *Service) MasterRestoreAccess(ctx context.Context, a Actor, typ string, id int64) error {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return err
	}
	if typ != "project" && typ != "asset" {
		return ErrNotFound
	}
	if _, err := parentAccess(ctx, s.Pool, p, typ, id, "", false); err != nil {
		return err
	}
	if !p.Allows(typ+".restore", All) || !p.Allows(typ+".archived.view", All) {
		return ErrForbidden
	}
	var eligible bool
	if typ == "project" {
		err = s.Pool.QueryRow(ctx, `SELECT p.deleted_at IS NOT NULL AND c.deleted_at IS NULL
		 FROM projects p JOIN customers c ON c.id=p.customer_id AND c.company_id=p.company_id
		 WHERE p.id=$1 AND p.company_id=$2`, id, p.CompanyID).Scan(&eligible)
	} else {
		err = s.Pool.QueryRow(ctx, `SELECT a.deleted_at IS NOT NULL
		 AND (a.customer_id IS NULL OR EXISTS(SELECT 1 FROM customers c WHERE c.id=a.customer_id AND c.company_id=a.company_id AND c.deleted_at IS NULL))
		 AND (a.location_id IS NULL OR EXISTS(SELECT 1 FROM locations l WHERE l.id=a.location_id AND l.company_id=a.company_id AND l.object_type='customer' AND l.object_id=a.customer_id AND l.deleted_at IS NULL))
		 FROM assets a WHERE a.id=$1 AND a.company_id=$2`, id, p.CompanyID).Scan(&eligible)
	}
	if err != nil {
		return err
	}
	if !eligible {
		return ErrConflict
	}
	return nil
}
