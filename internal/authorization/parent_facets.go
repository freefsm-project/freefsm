package authorization

import "context"

// ParentFacet permits a read-only independent facet on an already visible
// parent, including explicitly authorized archived records. Unlike an action,
// a facet does not require the parent itself to be mutable.
func (s *Service) ParentFacet(ctx context.Context, actor Actor, typ string, id int64, permission string) error {
	p, err := s.Principal(ctx, actor)
	if err != nil {
		return err
	}
	if _, err := parentAccess(ctx, s.Pool, p, typ, id, "", false); err != nil {
		return err
	}
	if p.Allows(permission, All) {
		return nil
	}
	if !p.Allows(permission, Assigned) {
		return ErrForbidden
	}
	jobID := id
	if typ == "estimate" || typ == "invoice" {
		if err := s.Pool.QueryRow(ctx, `SELECT coalesce(job_id,0) FROM `+typ+`s WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&jobID); err != nil {
			return err
		}
	} else if typ != "job" {
		return ErrForbidden
	}
	allowed, err := assignedJobAccess(ctx, s.Pool, p.UserID, jobID, false)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
