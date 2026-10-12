// Package authorization owns the migrated authorization boundary. Legacy Role
// strings are consulted only by the explicit, pending-cutover approval flow.
package authorization

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Scope string

const (
	All           Scope = "A"
	Assigned      Scope = "J"
	Context       Scope = "C"
	Own           Scope = "O"
	Configuration Scope = "U"
	Sealed        Scope = "S"
)

type Grant struct {
	Permission string `json:"permission"`
	Scope      Scope  `json:"scope"`
}
type Actor struct {
	UserID      int64
	SessionHash string
	Channel     string
}
type Service struct{ Pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Service { return &Service{Pool: pool} }

var (
	ErrForbidden = errors.New("permission denied")
	ErrNotFound  = errors.New("resource not found")
	ErrPending   = errors.New("authorization cutover requires Administrator review")
	ErrConflict  = errors.New("authorization changed; review again")
)

type Principal struct {
	UserID      int64
	RoleID      int64
	CompanyID   int64
	Ready       bool
	Protected   bool
	Grants      []Grant
	validScopes map[Grant]bool
}
type queryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func principal(ctx context.Context, q queryer, a Actor, transitional bool) (Principal, error) {
	p := Principal{Grants: []Grant{}, validScopes: map[Grant]bool{}}
	var legacy string
	var enabled bool
	var forcePasswordChange bool
	err := q.QueryRow(ctx, `SELECT u.id,coalesce(u.authorization_role_id,0),coalesce(u.company_id,0),u.role,u.is_active,s.ready,coalesce(r.protected,false),u.force_password_change FROM users u CROSS JOIN authorization_state s LEFT JOIN authorization_roles r ON r.id=u.authorization_role_id WHERE u.id=$1`, a.UserID).Scan(&p.UserID, &p.RoleID, &p.CompanyID, &legacy, &enabled, &p.Ready, &p.Protected, &forcePasswordChange)
	if err != nil || !enabled || forcePasswordChange || p.CompanyID <= 0 {
		return p, ErrForbidden
	}
	if a.SessionHash != "" {
		var valid bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND token_hash=$2 AND expires_at>clock_timestamp() AND revoked_at IS NULL AND kind=CASE WHEN $3='api' THEN 'mobile' ELSE 'web' END)`, a.UserID, a.SessionHash, a.Channel).Scan(&valid); err != nil || !valid {
			return p, ErrForbidden
		}
	}
	if !p.Ready {
		var approver bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_legacy_approvers WHERE user_id=$1)`, a.UserID).Scan(&approver); err != nil {
			return p, err
		}
		if transitional && legacy == "admin" && approver {
			p.Protected = true
			return p, nil
		}
		if transitional {
			return p, ErrForbidden
		}
		return p, ErrPending
	}
	rows, err := q.Query(ctx, `SELECT g.permission,g.scope,c.scope FROM authorization_grants g JOIN authorization_permissions c ON c.permission=g.permission WHERE g.role_id=$1`, p.RoleID)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	effective := map[Grant]bool{}
	for rows.Next() {
		var perm string
		var scope Scope
		var validScope Scope
		if err := rows.Scan(&perm, &scope, &validScope); err != nil {
			return p, err
		}
		grant := Grant{perm, scope}
		if !effective[grant] {
			p.Grants = append(p.Grants, grant)
			effective[grant] = true
		}
		p.validScopes[Grant{perm, validScope}] = true
	}
	return p, rows.Err()
}
func (s *Service) Principal(ctx context.Context, a Actor) (Principal, error) {
	return principal(ctx, s.Pool, a, false)
}
func (s *Service) Administrator(ctx context.Context, a Actor) (Principal, error) {
	p, e := principal(ctx, s.Pool, a, true)
	if e == nil && !p.Protected {
		e = ErrForbidden
	}
	return p, e
}
func (p Principal) Allows(permission string, scope Scope) bool {
	if !p.validScopes[Grant{permission, scope}] {
		return false
	}
	for _, g := range p.Grants {
		if g.Permission == permission && (g.Scope == scope || g.Scope == All && (scope == Context || scope == Assigned || scope == Own)) {
			return true
		}
	}
	return false
}

func (s *Service) transaction(ctx context.Context, a Actor, transition bool, fn func(pgx.Tx, Principal) error) error {
	return s.transact(ctx, a, transition, false, fn)
}

func (s *Service) transact(ctx context.Context, a Actor, transition, disablingSelf bool, fn func(pgx.Tx, Principal) error) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	p, err := principal(ctx, tx, a, transition)
	if err != nil {
		return err
	}
	if err = fn(tx, p); err != nil {
		return err
	}
	// A fresh read on the same transaction verifies identity and session before
	// commit. All authority-changing writers share the transaction mutex.
	if _, err = principal(ctx, tx, a, transition); err != nil {
		// A sealed self-disable deliberately consumes its own identity and
		// sessions. External revocations cannot commit under our mutex; the
		// deferred last-Administrator constraint still validates replacement.
		if !disablingSelf || !p.Protected || !errors.Is(err, ErrForbidden) {
			return err
		}
		var disabled bool
		if e := tx.QueryRow(ctx, `SELECT NOT is_active FROM users WHERE id=$1`, a.UserID).Scan(&disabled); e != nil || !disabled {
			return err
		}
	}
	return tx.Commit(ctx)
}
func audit(ctx context.Context, tx pgx.Tx, a Actor, p Principal, permission string, scope Scope, typ string, id int64, detail any) error {
	data, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	channel := a.Channel
	if channel == "" {
		channel = "web"
	}
	_, err = tx.Exec(ctx, `INSERT INTO authorization_audit(actor_id,role_id,permission,scope,target_type,target_id,channel,detail) VALUES($1,nullif($2,0),$3,$4,$5,$6,$7,$8)`, a.UserID, p.RoleID, permission, scope, typ, id, channel, data)
	return err
}
func validateGrants(ctx context.Context, tx pgx.Tx, grants []Grant) error {
	seen := map[Grant]bool{}
	for _, g := range grants {
		if seen[g] {
			return fmt.Errorf("duplicate permission/scope")
		}
		seen[g] = true
	}
	for _, g := range grants {
		var prereq *string
		if err := tx.QueryRow(ctx, `SELECT prerequisite FROM authorization_permissions WHERE permission=$1 AND scope=$2`, g.Permission, g.Scope).Scan(&prereq); err != nil || g.Scope == Sealed {
			return fmt.Errorf("unsupported or sealed grant: %s/%s", g.Permission, g.Scope)
		}
		if prereq != nil {
			requiredScope := g.Scope
			if g.Scope == Context && *prereq == "job.view" {
				requiredScope = Assigned
			}
			if !seen[Grant{*prereq, requiredScope}] && !(seen[Grant{*prereq, All}] && (requiredScope == Assigned || requiredScope == Context || requiredScope == Own)) {
				return fmt.Errorf("%s requires compatible %s", g.Permission, *prereq)
			}
		}
	}
	return nil
}

// SaveRole creates a Role when id is zero. Updating an existing Role requires
// exactly one expected revision from the displayed review, preventing stale
// editors from silently restoring a concurrently revoked grant.
func (s *Service) SaveRole(ctx context.Context, a Actor, id int64, name string, grants []Grant, expectedRevision ...int64) (int64, error) {
	err := s.transaction(ctx, a, true, func(tx pgx.Tx, p Principal) error {
		if !p.Protected {
			return ErrForbidden
		}
		if err := validateGrants(ctx, tx, grants); err != nil {
			return err
		}
		if id == 0 {
			if err := tx.QueryRow(ctx, `INSERT INTO authorization_roles(name) VALUES($1) RETURNING id`, name).Scan(&id); err != nil {
				return err
			}
		} else {
			if len(expectedRevision) != 1 {
				return ErrConflict
			}
			tag, err := tx.Exec(ctx, `UPDATE authorization_roles SET name=$2,revision=revision+1 WHERE id=$1 AND NOT protected AND revision=$3`, id, name, expectedRevision[0])
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1`, id); err != nil {
				return err
			}
		}
		for _, g := range grants {
			if _, err := tx.Exec(ctx, `INSERT INTO authorization_grants VALUES($1,$2,$3)`, id, g.Permission, g.Scope); err != nil {
				return err
			}
		}
		return audit(ctx, tx, a, p, "role.edit", Sealed, "role", id, map[string]any{"name": name, "grants": grants})
	})
	return id, err
}
