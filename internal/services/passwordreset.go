package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/passwordpolicy"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var ErrInvalidRecovery = errors.New("invalid or expired reset link")

type PasswordResetService struct{ pool *pgxpool.Pool }

func NewPasswordResetService(_ *ent.Client, pools ...*pgxpool.Pool) *PasswordResetService {
	s := &PasswordResetService{}
	if len(pools) > 0 {
		s.pool = pools[0]
	}
	return s
}

func credentialTransaction(ctx context.Context, pool *pgxpool.Pool, fn func(pgx.Tx) error) error {
	if pool == nil {
		return errors.New("credential database unavailable")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PasswordResetService) CreateToken(ctx context.Context, userID int64, expectedEmail ...string) (string, error) {
	return s.createToken(ctx, userID, expectedEmail, nil)
}

func (s *PasswordResetService) createToken(ctx context.Context, userID int64, expectedEmail []string, revision *int64) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	issue := func(tx pgx.Tx) error {
		var companyID int64
		var email string
		var currentRevision int64
		if err := tx.QueryRow(ctx, `SELECT company_id,email,credential_revision FROM users WHERE id=$1 AND is_active FOR UPDATE`, userID).Scan(&companyID, &email, &currentRevision); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrInvalidRecovery
			}
			return fmt.Errorf("read recovery identity: %w", err)
		}
		if len(expectedEmail) > 0 && expectedEmail[0] != email || revision != nil && *revision != currentRevision {
			return ErrInvalidRecovery
		}
		if _, err := tx.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id=$1 AND purpose='password-recovery-v1'`, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO password_reset_tokens(company_id,user_id,token_hash,purpose,created_at,expires_at,recipient,credential_revision) SELECT $1,$2,$3,'password-recovery-v1',issued,issued+interval '1 hour',$4,$5 FROM (SELECT clock_timestamp() AS issued) t`, companyID, userID, hashSessionToken(token), email, currentRevision)
		return err
	}
	var err error
	if r, ok := authorization.FromContext(ctx); ok {
		p, e := r.Service.Administrator(ctx, r.Actor)
		if e != nil {
			return "", e
		}
		err = r.Service.MutateAccountLink(ctx, r.Actor, p.CompanyID, userID, "user.recovery.resend", issue)
	} else {
		err = credentialTransaction(ctx, s.pool, func(tx pgx.Tx) error {
			if err := issue(tx); err != nil {
				return err
			}
			return protectedaudit.RecordRecoveryRequest(ctx, tx, userID)
		})
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *PasswordResetService) Validate(ctx context.Context, token string) (int64, error) {
	if s.pool == nil {
		return 0, ErrInvalidRecovery
	}
	var id int64
	err := s.pool.QueryRow(ctx, `SELECT t.user_id FROM password_reset_tokens t JOIN users u ON u.id=t.user_id WHERE token_hash=$1 AND purpose='password-recovery-v1' AND expires_at>clock_timestamp() AND u.is_active AND t.recipient=u.email AND t.credential_revision=u.credential_revision`, hashSessionToken(token)).Scan(&id)
	if err != nil {
		return 0, ErrInvalidRecovery
	}
	return id, nil
}

// Redeem consumes the proof, changes the credential, revokes every session and
// records immutable evidence in one serialized transaction.
func (s *PasswordResetService) Redeem(ctx context.Context, token, password string) error {
	return credentialTransaction(ctx, s.pool, func(tx pgx.Tx) error {
		var id, company, role int64
		var expires time.Time
		if err := tx.QueryRow(ctx, `SELECT u.id,u.company_id,coalesce(u.authorization_role_id,0),t.expires_at FROM users u JOIN password_reset_tokens t ON t.user_id=u.id WHERE t.token_hash=$1 AND t.purpose='password-recovery-v1' AND t.expires_at>clock_timestamp() AND u.is_active AND t.recipient=u.email AND t.credential_revision=u.credential_revision FOR UPDATE OF u,t`, hashSessionToken(token)).Scan(&id, &company, &role, &expires); err != nil {
			return ErrInvalidRecovery
		}
		if err := setCredential(ctx, tx, id, company, password, ""); err != nil {
			return err
		}
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT $1::timestamptz>clock_timestamp()`, expires).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return ErrInvalidRecovery
		}
		return protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: id, RoleID: role, Action: "user.password.recover", Scope: "O", TargetType: "user", TargetID: id, Channel: "recovery", Detail: map[string]string{"proof": "password-recovery-v1"}})
	})
}

func setCredential(ctx context.Context, tx pgx.Tx, id, company int64, password, keepSession string) error {
	policy, err := passwordpolicy.Load(ctx, tx, company)
	if err != nil {
		return err
	}
	if err = policy.Validate(password); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET password_hash=$2,force_password_change=false,updated_at=clock_timestamp() WHERE id=$1`, id, string(hash)); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1 AND NOT (token_hash=$2 AND kind='web')`, id, keepSession); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM password_reset_tokens WHERE user_id=$1 AND purpose='password-recovery-v1'`, id)
	return err
}

// ChangeOwnPassword is intrinsic, including during forced change; it requires
// a live browser session and rechecks the current password under the same lock
// as disablement, Administrator resets, grants and recovery.
func (s *UserService) ChangeOwnPassword(ctx context.Context, id int64, session, current, password string) error {
	return credentialTransaction(ctx, s.pool, func(tx pgx.Tx) error {
		var company, role int64
		var hash string
		if err := tx.QueryRow(ctx, `SELECT company_id,coalesce(authorization_role_id,0),password_hash FROM users WHERE id=$1 AND is_active FOR UPDATE`, id).Scan(&company, &role, &hash); err != nil {
			return authorization.ErrForbidden
		}
		var valid bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND token_hash=$2 AND kind='web' AND revoked_at IS NULL AND expires_at>clock_timestamp())`, id, hashSessionToken(session)).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return authorization.ErrForbidden
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
			return errors.New("current password incorrect")
		}
		if err := setCredential(ctx, tx, id, company, password, hashSessionToken(session)); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND token_hash=$2 AND expires_at>clock_timestamp() AND revoked_at IS NULL)`, id, hashSessionToken(session)).Scan(&valid); err != nil {
			return err
		}
		if !valid {
			return authorization.ErrForbidden
		}
		return protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: id, RoleID: role, Action: "user.password.change", Scope: "O", TargetType: "user", TargetID: id, Channel: "web"})
	})
}
