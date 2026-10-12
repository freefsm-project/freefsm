package authorization

import (
	"context"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/jackc/pgx/v5"
)

// fileAccess uses the parent's basic View, never financial or closed Context.
// The table names are a closed product allowlist, not request SQL.
func fileAccess(ctx context.Context, q queryer, p Principal, a Actor, ref objectref.Ref, permission string, uploader int64, lock bool) (Scope, error) {
	table := map[string]string{"customer": "customers", "project": "projects", "job": "jobs", "asset": "assets", "estimate": "estimates", "invoice": "invoices", "lease": "leases"}[ref.ObjectType()]
	if table == "" || !ref.Valid() {
		return "", ErrNotFound
	}
	jobExpr, hidden := "0::bigint", "true"
	if ref.ObjectType() == "job" {
		jobExpr = "id"
	}
	if ref.ObjectType() == "estimate" || ref.ObjectType() == "invoice" {
		jobExpr = "coalesce(job_id,0)"
		hidden = "conversion_hidden_at IS NULL"
	}
	suffix := ""
	if lock {
		suffix = " FOR SHARE"
	}
	var active, visible bool
	var jobID int64
	err := q.QueryRow(ctx, `SELECT deleted_at IS NULL,`+hidden+`,`+jobExpr+` FROM `+table+` WHERE id=$1 AND company_id=$2`+suffix, ref.ID, p.CompanyID).Scan(&active, &visible, &jobID)
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if !visible {
		return "", ErrNotFound
	}
	if a.Channel == "api" {
		if jobID == 0 {
			return "", ErrNotFound
		}
		if (permission == "file.rename" || permission == "file.remove") && uploader != a.UserID {
			return "", ErrForbidden
		}
	}
	parentView := ref.ObjectType() + ".view"
	all := p.Allows(parentView, All)
	assigned := false
	if jobID != 0 && active && p.Allows(parentView, Assigned) {
		var id int64
		err := q.QueryRow(ctx, `SELECT j.id FROM jobs j JOIN job_assignments ja ON ja.job_id=j.id WHERE j.id=$1 AND ja.user_id=$2 AND j.company_id=$3 AND j.deleted_at IS NULL`+suffix, jobID, a.UserID, p.CompanyID).Scan(&id)
		if err != nil && err != pgx.ErrNoRows {
			return "", err
		}
		assigned = err == nil
	}
	if !all && !assigned || !active && (!all || !p.Allows(ref.ObjectType()+".archived.view", All)) {
		return "", ErrNotFound
	}
	if permission == "" {
		if all {
			return All, nil
		}
		return Assigned, nil
	}
	if permission != "file.download" && !active {
		return "", ErrForbidden
	}
	if p.Allows(permission, All) {
		return All, nil
	}
	if assigned && p.Allows(permission, Assigned) {
		return Assigned, nil
	}
	if uploader == a.UserID && (permission == "file.rename" || permission == "file.remove") && p.Allows(permission, Own) {
		return Own, nil
	}
	return "", ErrForbidden
}

func (s *Service) FileAccess(ctx context.Context, a Actor, ref objectref.Ref, permission string, uploader int64) error {
	p, err := s.Principal(ctx, a)
	if err != nil {
		return err
	}
	_, err = fileAccess(ctx, s.Pool, p, a, ref, permission, uploader, false)
	return err
}

// FileAction serializes with grant/session changes and locks the exact parent
// and assignment until the File mutation and immutable audit commit together.
// A zero returned effect ID represents a desired-state no-op (no new audit).
func (s *Service) FileAction(ctx context.Context, a Actor, ref objectref.Ref, permission string, uploader int64, fn func(pgx.Tx) (int64, any, error)) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		scope, err := fileAccess(ctx, tx, p, a, ref, permission, uploader, true)
		if err != nil {
			return err
		}
		id, detail, err := fn(tx)
		if err != nil {
			return err
		}
		if id == 0 {
			return nil
		}
		return audit(ctx, tx, a, p, permission, scope, "file", id, detail)
	})
}
