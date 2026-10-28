package authorization

import (
	"context"

	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/jackc/pgx/v5"
)

// ParentAccess is the shared visibility/action prerequisite for the integrated
// collaboration parents. Read authorization neither locks writers nor loads a
// Custom Fields payload. Mutations use the same rules with transaction locks.
func (s *Service) ParentAccess(ctx context.Context, a Actor, typ string, id int64, permission string) error {
	if typ == "lease" && a.Channel == "api" {
		return ErrNotFound
	}
	p, err := s.Principal(ctx, a)
	if err != nil {
		return err
	}
	_, err = parentAccess(ctx, s.Pool, p, typ, id, permission, false)
	return err
}

// WithinParent holds the authority fence and record/assignment locks while a
// domain reads a coherent action snapshot or commits an authorized effect.
func (s *Service) WithinParent(ctx context.Context, a Actor, typ string, id int64, permission string, fn func(pgx.Tx, Principal) error) error {
	if typ == "lease" && a.Channel == "api" {
		return ErrNotFound
	}
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if _, err := parentAccess(ctx, tx, p, typ, id, permission, true); err != nil {
			return err
		}
		return fn(tx, p)
	})
}

// CheckParentTransaction authorizes a domain-owned effect inside WithinActor's
// committing transaction, including current parent and assignment row locks.
func CheckParentTransaction(ctx context.Context, tx pgx.Tx, p Principal, typ string, id int64, permission string) (Scope, error) {
	return parentAccess(ctx, tx, p, typ, id, permission, true)
}

func parentAccess(ctx context.Context, q queryer, p Principal, typ string, id int64, permission string, locking bool) (Scope, error) {
	table, ok := customfields.ParentTable(typ)
	if !ok {
		return "", ErrNotFound
	}
	jobExpr := "0"
	if typ == "job" {
		jobExpr = "id"
	} else if typ == "invoice" || typ == "estimate" {
		jobExpr = "coalesce(job_id,0)"
	}
	hiddenExpr := "false"
	if typ == "invoice" || typ == "estimate" {
		hiddenExpr = "conversion_hidden_at IS NOT NULL"
	}
	lock := ""
	if locking {
		lock = " FOR UPDATE"
	}
	var company, job int64
	var active, hidden bool
	err := q.QueryRow(ctx, `SELECT company_id,`+jobExpr+`,deleted_at IS NULL,`+hiddenExpr+` FROM `+table+` WHERE id=$1`+lock, id).Scan(&company, &job, &active, &hidden)
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if company != p.CompanyID || hidden {
		return "", ErrNotFound
	}
	scope := All
	if !p.Allows(typ+".view", All) {
		if job == 0 || !p.Allows(typ+".view", Assigned) {
			return "", ErrNotFound
		}
		assigned, err := assignedJobAccess(ctx, q, p.UserID, job, locking)
		if err != nil {
			return "", err
		}
		if !assigned {
			return "", ErrNotFound
		}
		scope = Assigned
	}
	if !active {
		if !p.Allows(typ+".archived.view", All) {
			return "", ErrNotFound
		}
		if permission != "" {
			return "", ErrForbidden
		}
	}
	if permission != "" && !p.Allows(permission, scope) {
		if job == 0 || !p.Allows(permission, Assigned) {
			return "", ErrForbidden
		}
		assigned, err := assignedJobAccess(ctx, q, p.UserID, job, locking)
		if err != nil {
			return "", err
		}
		if !assigned {
			return "", ErrForbidden
		}
		scope = Assigned
	}
	return scope, nil
}

func assignedJobAccess(ctx context.Context, q queryer, userID, jobID int64, locking bool) (bool, error) {
	lock := ""
	if locking {
		lock = " FOR SHARE OF a,j"
	}
	var assigned bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_assignments a JOIN jobs j ON j.id=a.job_id WHERE a.job_id=$1 AND a.user_id=$2 AND j.deleted_at IS NULL`+lock+`)`, jobID, userID).Scan(&assigned)
	return assigned, err
}
