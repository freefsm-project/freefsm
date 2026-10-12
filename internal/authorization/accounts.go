package authorization

import (
	"context"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
)

type NewAccount struct {
	Name, Email, PasswordHash string
	RoleID                    int64
	Invited                   bool
}

// MutateAccountLink provides account-link services a transaction-bound sealed
// authorization boundary. The callback owns token semantics; the boundary owns
// current authority, target identity, serialization and immutable audit.
func (s *Service) MutateAccountLink(ctx context.Context, a Actor, companyID, userID int64, action string, mutate func(pgx.Tx) error) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Protected || p.CompanyID != companyID {
			return ErrForbidden
		}
		switch action {
		case "user.invite", "user.welcome.resend", "user.recovery.resend":
		default:
			return ErrForbidden
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND company_id=$2)`, userID, companyID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := mutate(tx); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, action, Sealed, "user", userID, nil)
	})
}

func (s *Service) CreateAccount(ctx context.Context, a Actor, in NewAccount) (int64, error) {
	var id int64
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Protected {
			return ErrForbidden
		}
		address, err := mail.ParseAddress(in.Email)
		if strings.TrimSpace(in.Name) == "" || err != nil || address.Address != in.Email || in.RoleID <= 0 {
			return ErrConflict
		}
		// The retained legacy discriminator is not authority. New accounts always
		// carry an explicit Role identity and never acquire grants from that label.
		err = tx.QueryRow(ctx, `INSERT INTO users(company_id,name,email,password_hash,role,authorization_role_id,is_active,force_password_change,onboarding_completed_at,welcome_email_sent_at) VALUES($1,$2,$3,$4,'tech',$5,$6,$6,CASE WHEN $6 THEN clock_timestamp() END,CASE WHEN NOT $6 THEN clock_timestamp() END) RETURNING id`, p.CompanyID, strings.TrimSpace(in.Name), in.Email, in.PasswordHash, in.RoleID, !in.Invited).Scan(&id)
		if err != nil {
			return err
		}
		action := "user.create"
		if in.Invited {
			action = "user.invite"
		}
		return audit(ctx, tx, a, p, action, Sealed, "user", id, map[string]any{"role_id": in.RoleID, "invited": in.Invited})
	})
	return id, err
}

func (s *Service) ResetAccountPassword(ctx context.Context, a Actor, id int64, hash string) error {
	return s.transact(ctx, a, false, id == a.UserID, func(tx pgx.Tx, p Principal) error {
		if !p.Protected {
			return ErrForbidden
		}
		tag, err := tx.Exec(ctx, `UPDATE users SET password_hash=$3,force_password_change=true WHERE id=$1 AND company_id=$2`, id, p.CompanyID, hash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id=$1`, id); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "user.password.require_change", Sealed, "user", id, map[string]bool{"temporary_password_set": true})
	})
}

func (s *Service) AccountRoleNames(ctx context.Context, a Actor) (map[int64]string, error) {
	p, err := s.Administrator(ctx, a)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT u.id,r.name FROM users u JOIN authorization_roles r ON r.id=u.authorization_role_id WHERE u.company_id=$1`, p.CompanyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

func (s *Service) EditIdentity(ctx context.Context, a Actor, id int64, name, email string) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Protected {
			return ErrForbidden
		}
		name, email = strings.TrimSpace(name), strings.TrimSpace(email)
		address, err := mail.ParseAddress(email)
		if name == "" || err != nil || address.Address != email {
			return ErrConflict
		}
		var beforeName, beforeEmail string
		if err := tx.QueryRow(ctx, `SELECT name,email FROM users WHERE id=$1 AND company_id=$2`, id, p.CompanyID).Scan(&beforeName, &beforeEmail); err != nil {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET name=$2,email=$3 WHERE id=$1`, id, name, email); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "user.identity.edit", Sealed, "user", id, map[string]any{"before": map[string]string{"name": beforeName, "email": beforeEmail}, "after": map[string]string{"name": name, "email": email}})
	})
}

// AccountSecurity performs sealed actions under the same mutex as session and
// grant changes, committing the security state, revocations and audit together.
func (s *Service) AccountSecurity(ctx context.Context, a Actor, id int64, action string) error {
	return s.transact(ctx, a, false, id == a.UserID, func(tx pgx.Tx, p Principal) error {
		if !p.Protected {
			return ErrForbidden
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND company_id=$2)`, id, p.CompanyID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		switch action {
		case "user.password.require_change":
			if _, err := tx.Exec(ctx, `UPDATE users SET force_password_change=true WHERE id=$1`, id); err != nil {
				return err
			}
		case "user.sessions.revoke":
		default:
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, id); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, action, Sealed, "user", id, nil)
	})
}
