package authorization

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type Role struct {
	GrantError string  `json:"grant_error,omitempty"`
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Protected  bool    `json:"protected"`
	Revision   int64   `json:"revision"`
	Grants     []Grant `json:"grants"`
}
type ReviewUser struct {
	Revision   time.Time `json:"revision"`
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	Email      string    `json:"email"`
	LegacyRole string    `json:"legacy_role"`
	RoleID     *int64    `json:"role_id"`
	Reviewed   bool      `json:"reviewed"`
}
type Review struct {
	Catalog  []GrantOption `json:"catalog"`
	Snapshot string        `json:"snapshot"`
	Ready    bool          `json:"ready"`
	Roles    []Role        `json:"roles"`
	Users    []ReviewUser  `json:"users"`
}

func (s *Service) Review(ctx context.Context, a Actor) (Review, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Review{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	p, err := principal(ctx, tx, a, true)
	if err != nil {
		return Review{}, err
	}
	if !p.Protected {
		return Review{}, ErrForbidden
	}
	result, err := loadReview(ctx, tx, p)
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}

func loadReview(ctx context.Context, q queryer, p Principal) (Review, error) {
	result := Review{Ready: p.Ready, Roles: []Role{}, Users: []ReviewUser{}}
	var err error
	result.Catalog, err = grantOptions(ctx, q)
	if err != nil {
		return result, err
	}
	rows, err := q.Query(ctx, `SELECT id,name,protected,revision FROM authorization_roles ORDER BY id`)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Protected, &r.Revision); err != nil {
			rows.Close()
			return result, err
		}
		r.Grants = []Grant{}
		result.Roles = append(result.Roles, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	for i := range result.Roles {
		rows, err := q.Query(ctx, `SELECT permission,scope FROM authorization_grants WHERE role_id=$1 ORDER BY permission,scope`, result.Roles[i].ID)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var g Grant
			if err := rows.Scan(&g.Permission, &g.Scope); err != nil {
				rows.Close()
				return result, err
			}
			result.Roles[i].Grants = append(result.Roles[i].Grants, g)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return result, err
		}
		if !result.Roles[i].Protected {
			if err := validateGrants(ctx, q, result.Roles[i].Grants); err != nil {
				result.Roles[i].GrantError = err.Error()
			}
		}
	}
	rows, err = q.Query(ctx, `SELECT u.id,u.name,u.email,u.role,CASE WHEN s.ready THEN u.authorization_role_id ELSE v.role_id END,coalesce(v.user_revision=u.updated_at AND v.role_revision=r.revision,false),u.updated_at FROM users u CROSS JOIN authorization_state s LEFT JOIN authorization_reviews v ON v.user_id=u.id LEFT JOIN authorization_roles r ON r.id=v.role_id ORDER BY u.id`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var u ReviewUser
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.LegacyRole, &u.RoleID, &u.Reviewed, &u.Revision); err != nil {
			return result, err
		}
		result.Users = append(result.Users, u)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	b, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(b)
	result.Snapshot = hex.EncodeToString(sum[:])
	return result, nil
}
func (s *Service) ApproveUser(ctx context.Context, a Actor, userID, roleID int64, reviewedSnapshot string) error {
	return s.transaction(ctx, a, true, func(tx pgx.Tx, p Principal) error {
		if !p.Protected {
			return ErrForbidden
		}
		if p.Ready {
			return ErrConflict
		}
		shown, err := loadReview(ctx, tx, p)
		if err != nil {
			return err
		}
		if shown.Snapshot != reviewedSnapshot {
			return ErrConflict
		}
		for _, role := range shown.Roles {
			if role.ID == roleID && role.GrantError != "" {
				return ErrConflict
			}
		}
		tag, err := tx.Exec(ctx, `INSERT INTO authorization_reviews(user_id,role_id,user_revision,role_revision,approved_by)
 SELECT u.id,r.id,u.updated_at,r.revision,$3 FROM users u CROSS JOIN authorization_roles r WHERE u.id=$1 AND r.id=$2
 ON CONFLICT(user_id) DO UPDATE SET role_id=excluded.role_id,user_revision=excluded.user_revision,role_revision=excluded.role_revision,approved_by=excluded.approved_by,approved_at=clock_timestamp()`, userID, roleID, a.UserID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return audit(ctx, tx, a, p, "cutover.user.approve", Sealed, "user", userID, map[string]int64{"role_id": roleID})
	})
}
func (s *Service) Cutover(ctx context.Context, a Actor) error {
	return s.transaction(ctx, a, true, func(tx pgx.Tx, p Principal) error {
		if !p.Protected {
			return ErrForbidden
		}
		if p.Ready {
			return ErrConflict
		}
		var unresolved bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u LEFT JOIN authorization_reviews v ON v.user_id=u.id LEFT JOIN authorization_roles r ON r.id=v.role_id WHERE v.user_id IS NULL OR v.user_revision<>u.updated_at OR v.role_revision<>r.revision)`).Scan(&unresolved); err != nil {
			return err
		}
		if unresolved {
			return ErrConflict
		}
		review, err := loadReview(ctx, tx, p)
		if err != nil {
			return err
		}
		invalid := map[int64]bool{}
		for _, role := range review.Roles {
			invalid[role.ID] = role.GrantError != ""
		}
		for _, user := range review.Users {
			if user.RoleID == nil || invalid[*user.RoleID] {
				return ErrConflict
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE users u SET authorization_role_id=v.role_id FROM authorization_reviews v WHERE v.user_id=u.id`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE authorization_state SET ready=true,bootstrapped=true`); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "cutover.commit", Sealed, "instance", 1, nil)
	})
}
func (s *Service) AssignUser(ctx context.Context, a Actor, userID, roleID int64) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Protected {
			return ErrForbidden
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET authorization_role_id=$2 WHERE id=$1`, userID, roleID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		return audit(ctx, tx, a, p, "user.role.assign", Sealed, "user", userID, map[string]int64{"role_id": roleID})
	})
}
func (s *Service) SetEnabled(ctx context.Context, a Actor, userID int64, enabled bool) error {
	return s.transact(ctx, a, false, userID == a.UserID && !enabled, func(tx pgx.Tx, p Principal) error {
		if !p.Protected {
			return ErrForbidden
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET is_active=$2 WHERE id=$1`, userID, enabled)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		if !enabled {
			if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id=$1 AND purpose='password-recovery-v1'`, userID); err != nil {
				return err
			}
		}
		return audit(ctx, tx, a, p, "user.enabled.edit", Sealed, "user", userID, map[string]bool{"enabled": enabled})
	})
}
func (s *Service) RemoveRole(ctx context.Context, a Actor, id, replacement int64) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Protected || id == replacement {
			return ErrForbidden
		}
		rows, err := tx.Query(ctx, `UPDATE users SET authorization_role_id=$2 WHERE authorization_role_id=$1 RETURNING id`, id, replacement)
		if err != nil {
			return err
		}
		users, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM authorization_reviews WHERE role_id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1`, id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM authorization_roles WHERE id=$1 AND NOT protected`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrForbidden
		}
		return audit(ctx, tx, a, p, "role.remove", Sealed, "role", id, map[string]any{"replacement": replacement, "user_ids": users})
	})
}
