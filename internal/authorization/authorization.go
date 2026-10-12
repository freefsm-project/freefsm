// Package authorization owns the migrated authorization boundary. Legacy Role
// strings are consulted only by the explicit, pending-cutover approval flow.
package authorization

import (
	"context"
	"errors"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/freefsm-project/freefsm/internal/saveaction"

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
	ErrForbidden   = errors.New("permission denied")
	ErrNotFound    = errors.New("resource not found")
	ErrPending     = errors.New("authorization cutover requires Administrator review")
	ErrConflict    = errors.New("authorization changed; review again")
	ErrUnavailable = errors.New("authorization dependency unavailable")
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
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return p, fmt.Errorf("%w: load principal: %w", ErrUnavailable, err)
	}
	if err != nil || !enabled || forcePasswordChange || p.CompanyID <= 0 {
		return p, ErrForbidden
	}
	if a.SessionHash != "" {
		var valid bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND token_hash=$2 AND expires_at>clock_timestamp() AND revoked_at IS NULL AND kind=CASE WHEN $3='api' THEN 'mobile' ELSE 'web' END)`, a.UserID, a.SessionHash, a.Channel).Scan(&valid); err != nil {
			return p, fmt.Errorf("%w: check session: %w", ErrUnavailable, err)
		}
		if !valid {
			return p, ErrForbidden
		}
	}
	if !p.Ready {
		var approver bool
		if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_legacy_approvers WHERE user_id=$1)`, a.UserID).Scan(&approver); err != nil {
			return p, fmt.Errorf("%w: %w", ErrUnavailable, err)
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
	c, err := loadCatalog(ctx, q)
	if err != nil {
		return p, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	for g := range c {
		p.validScopes[g] = true
	}
	rows, err := q.Query(ctx, `SELECT permission,scope FROM authorization_grants WHERE role_id=$1 ORDER BY permission,scope`, p.RoleID)
	if err != nil {
		return p, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer rows.Close()
	for rows.Next() {
		var grant Grant
		if err := rows.Scan(&grant.Permission, &grant.Scope); err != nil {
			return p, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if grant.Scope == Sealed && !p.Protected {
			continue
		}
		p.Grants = append(p.Grants, grant)
	}
	p.Grants = c.effective(p.Grants)
	if err := rows.Err(); err != nil {
		return p, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return p, nil
}
func (s *Service) Principal(ctx context.Context, a Actor) (Principal, error) {
	return principal(ctx, s.Pool, a, false)
}

// WithinPermission holds the shared authority lock through effects and commit.
// Domain services retain ownership of invariants and transaction-bound audit.
func (s *Service) WithinPermission(ctx context.Context, a Actor, permission string, scope Scope, fn func(pgx.Tx, Principal) error) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows(permission, scope) {
			return ErrForbidden
		}
		return fn(tx, p)
	})
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

func (s *Service) transact(ctx context.Context, a Actor, transition, consumingSelf bool, fn func(pgx.Tx, Principal) error) error {
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
	// Capture the pre-write revision under the same fence. Let the command's
	// current object/permission checks take precedence over comparison errors;
	// otherwise a revoked editor could retain inaccessible input on a conflict.
	guardErr := saveaction.Check(ctx, tx)
	if err = fn(tx, p); err != nil {
		saveaction.RolledBack(ctx)
		return err
	}
	if guardErr != nil {
		saveaction.RolledBack(ctx)
		return guardErr
	}
	// A fresh read on the same transaction verifies identity and session before
	// commit. All authority-changing writers share the transaction mutex.
	if _, err = principal(ctx, tx, a, transition); err != nil {
		// A sealed self-security action deliberately consumes its own identity and
		// sessions. External revocations cannot commit under our mutex; the
		// deferred last-Administrator constraint still validates replacement.
		if !consumingSelf || !p.Protected || !errors.Is(err, ErrForbidden) {
			return err
		}
		var disabled bool
		if e := tx.QueryRow(ctx, `SELECT NOT is_active OR force_password_change OR ($2<>'' AND NOT EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND token_hash=$2 AND revoked_at IS NULL)) FROM users WHERE id=$1`, a.UserID, a.SessionHash).Scan(&disabled); e != nil || !disabled {
			return err
		}
	}
	if err := saveaction.ConfirmDomain(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func audit(ctx context.Context, tx pgx.Tx, a Actor, p Principal, permission string, scope Scope, typ string, id int64, detail any) error {
	channel := a.Channel
	if channel == "" {
		channel = "web"
	}
	return protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: a.UserID, RoleID: p.RoleID, Action: permission, Scope: string(scope), TargetType: typ, TargetID: id, Channel: channel, Detail: detail})
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
