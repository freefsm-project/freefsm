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
	return parentFacet(ctx, s.Pool, p, typ, id, permission, false)
}

// parentFacet also supports callers already holding the authority fence: use
// their transaction and current principal rather than opening a separate read.
func parentFacet(ctx context.Context, q queryer, p Principal, typ string, id int64, permission string, locking bool) error {
	if _, err := parentAccess(ctx, q, p, typ, id, "", locking); err != nil {
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
		if err := q.QueryRow(ctx, `SELECT coalesce(job_id,0) FROM `+typ+`s WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&jobID); err != nil {
			return err
		}
	} else if typ != "job" {
		return ErrForbidden
	}
	allowed, err := assignedJobAccess(ctx, q, p.UserID, jobID, locking)
	if err != nil {
		return err
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
