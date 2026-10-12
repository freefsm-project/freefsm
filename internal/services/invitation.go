package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/invitationtoken"
	"github.com/freefsm-project/freefsm/internal/ent/predicate"
	"github.com/freefsm-project/freefsm/internal/ent/user"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidInvitation       = errors.New("invalid or expired invitation")
	ErrInvitationPassword      = errors.New("invitation password does not satisfy current policy")
	ErrWelcomeResendIneligible = errors.New("welcome invitation cannot be resent")
)

type InvitationTarget struct {
	UserID    int64
	CompanyID int64
}

type InvitationService struct {
	client *ent.Client
	pool   *pgxpool.Pool
}

func NewInvitationService(client *ent.Client, pool ...*pgxpool.Pool) *InvitationService {
	s := &InvitationService{client: client}
	if len(pool) > 0 {
		s.pool = pool[0]
	}
	return s
}

func (s *InvitationService) CreateInvite(ctx context.Context, companyID, userID int64) (string, error) {
	if request, ok := authorization.FromContext(ctx); ok {
		return s.sealedInvite(ctx, request, companyID, userID, "user.invite")
	}
	if s.pool != nil {
		return "", authorization.ErrForbidden
	}
	if companyID <= 0 {
		return "", ErrWelcomeResendIneligible
	}

	tx, err := s.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return "", fmt.Errorf("begin invitation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.User.Query().Where(welcomeInviteTarget(companyID, userID)...).Only(ctx)
	if ent.IsNotFound(err) {
		return "", ErrWelcomeResendIneligible
	}
	if err != nil {
		return "", fmt.Errorf("check invitation target: %w", err)
	}

	now := time.Now()
	if err := invalidateOpenInvites(ctx, tx.Client(), companyID, userID, now); err != nil {
		return "", err
	}
	token, err := createInvite(ctx, tx.Client(), companyID, userID, now)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit invitation transaction: %w", err)
	}
	return token, nil
}

func (s *InvitationService) CanResendWelcome(ctx context.Context, companyID, userID int64) (bool, error) {
	if companyID <= 0 {
		return false, nil
	}
	return s.client.User.Query().Where(welcomeInviteTarget(companyID, userID)...).Exist(ctx)
}

func (s *InvitationService) RenewPendingInvite(ctx context.Context, companyID, userID int64) (*ent.User, string, error) {
	if request, ok := authorization.FromContext(ctx); ok {
		token, err := s.sealedInvite(ctx, request, companyID, userID, "user.welcome.resend")
		if err != nil {
			return nil, "", err
		}
		u, err := s.client.User.Get(ctx, userID)
		return u, token, err
	}
	if s.pool != nil {
		return nil, "", authorization.ErrForbidden
	}
	if companyID <= 0 {
		return nil, "", ErrWelcomeResendIneligible
	}

	tx, err := s.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, "", fmt.Errorf("begin invitation renewal transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	u, err := tx.User.Query().Where(welcomeInviteTarget(companyID, userID)...).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, "", ErrWelcomeResendIneligible
	}
	if err != nil {
		return nil, "", fmt.Errorf("check invitation renewal target: %w", err)
	}

	now := time.Now()
	if err := invalidateOpenInvites(ctx, tx.Client(), companyID, userID, now); err != nil {
		return nil, "", err
	}
	token, err := createInvite(ctx, tx.Client(), companyID, userID, now)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(); err != nil {
		return nil, "", fmt.Errorf("commit invitation renewal: %w", err)
	}
	return u, token, nil
}

func (s *InvitationService) sealedInvite(ctx context.Context, request authorization.Request, companyID, userID int64, action string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	err := request.Service.MutateAccountLink(ctx, request.Actor, companyID, userID, action, func(tx pgx.Tx) error {
		var eligible bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND company_id=$2 AND NOT is_active AND onboarding_completed_at IS NULL)`, userID, companyID).Scan(&eligible); err != nil {
			return err
		}
		if !eligible {
			return ErrWelcomeResendIneligible
		}
		if _, err := tx.Exec(ctx, `UPDATE invitation_tokens SET consumed_at=clock_timestamp() WHERE user_id=$1 AND company_id=$2 AND consumed_at IS NULL`, userID, companyID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO invitation_tokens(company_id,user_id,token_hash,created_at,expires_at) VALUES($1,$2,$3,statement_timestamp(),statement_timestamp()+interval '72 hours')`, companyID, userID, tokenHash(token)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE onboarding_migration_review SET reviewed_at=coalesce(reviewed_at,clock_timestamp()) WHERE user_id=$1`, userID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

// NeedsOnboardingReview exposes only an Administrator-facing diagnostic, never
// token contents or an invented activation event.
func (s *InvitationService) NeedsOnboardingReview(ctx context.Context, companyID, userID int64) (bool, error) {
	if s.pool == nil {
		return false, nil
	}
	var pending bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM onboarding_migration_review r JOIN users u ON u.id=r.user_id WHERE u.id=$1 AND u.company_id=$2 AND r.reviewed_at IS NULL)`, userID, companyID).Scan(&pending)
	return pending, err
}

func (s *InvitationService) ValidateInvite(ctx context.Context, token string) (InvitationTarget, error) {
	now := time.Now()
	t, err := s.client.InvitationToken.Query().Where(validInvitationToken(tokenHash(token), now)...).Only(ctx)
	if ent.IsNotFound(err) {
		return InvitationTarget{}, ErrInvalidInvitation
	}
	if err != nil {
		return InvitationTarget{}, fmt.Errorf("validate invitation token: %w", err)
	}
	companyID := *t.CompanyID
	eligible, err := s.client.User.Query().Where(invitationAcceptanceTarget(companyID, t.UserID)...).Exist(ctx)
	if err != nil {
		return InvitationTarget{}, fmt.Errorf("validate invitation user: %w", err)
	}
	if !eligible {
		return InvitationTarget{}, ErrInvalidInvitation
	}
	return InvitationTarget{UserID: t.UserID, CompanyID: companyID}, nil
}

func (s *InvitationService) AcceptInvite(ctx context.Context, token, password string) (*ent.User, error) {
	if s.pool != nil {
		return s.acceptPersistedInvitation(ctx, token, password)
	}
	tx, err := s.client.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, fmt.Errorf("begin invitation acceptance transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now()
	hash := tokenHash(token)
	t, err := tx.InvitationToken.Query().Where(validInvitationToken(hash, now)...).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrInvalidInvitation
	}
	if err != nil {
		return nil, fmt.Errorf("revalidate invitation token: %w", err)
	}
	companyID := *t.CompanyID
	u, err := tx.User.Query().Where(invitationAcceptanceTarget(companyID, t.UserID)...).Only(ctx)
	if ent.IsNotFound(err) {
		return nil, ErrInvalidInvitation
	}
	if err != nil {
		return nil, fmt.Errorf("revalidate invitation user: %w", err)
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash invitation password: %w", err)
	}

	consumed, err := tx.InvitationToken.Update().Where(
		invitationtoken.IDEQ(t.ID),
		invitationtoken.CompanyIDEQ(companyID),
		invitationtoken.TokenHashEQ(hash),
		invitationtoken.ExpiresAtGT(now),
		invitationtoken.ConsumedAtIsNil(),
	).SetConsumedAt(now).Save(ctx)
	if err != nil {
		if invitationTransactionConflict(err) {
			return nil, ErrInvalidInvitation
		}
		return nil, fmt.Errorf("consume invitation: %w", err)
	}
	if consumed != 1 {
		return nil, ErrInvalidInvitation
	}

	updated, err := tx.User.Update().Where(invitationAcceptanceTarget(companyID, u.ID)...).
		SetPasswordHash(string(passwordHash)).
		SetIsActive(true).
		SetOnboardingCompletedAt(now).
		SetForcePasswordChange(false).
		Save(ctx)
	if err != nil {
		if invitationTransactionConflict(err) {
			return nil, ErrInvalidInvitation
		}
		return nil, fmt.Errorf("activate invited user: %w", err)
	}
	if updated != 1 {
		return nil, ErrInvalidInvitation
	}
	if err := tx.Commit(); err != nil {
		if invitationTransactionConflict(err) {
			return nil, ErrInvalidInvitation
		}
		return nil, fmt.Errorf("commit invitation acceptance: %w", err)
	}

	u.PasswordHash = string(passwordHash)
	u.IsActive = true
	u.OnboardingCompletedAt = &now
	u.ForcePasswordChange = false
	return u, nil
}

func welcomeInviteTarget(companyID, userID int64) []predicate.User {
	return []predicate.User{
		user.IDEQ(userID),
		user.CompanyIDEQ(companyID),
		user.OnboardingCompletedAtIsNil(),
		user.IsActiveEQ(false),
	}
}

// RecordMailAccepted records only an observed transport handoff, never inbox
// delivery. A failed record is reported separately from a failed handoff.
func (s *InvitationService) RecordMailAccepted(ctx context.Context, companyID, userID int64) error {
	if request, ok := authorization.FromContext(ctx); ok {
		return request.Service.MutateAccountLink(ctx, request.Actor, companyID, userID, "user.invitation.mail.accepted", func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE users SET welcome_email_sent_at=clock_timestamp() WHERE id=$1 AND company_id=$2`, userID, companyID)
			return err
		})
	}
	if s.pool != nil {
		return authorization.ErrForbidden
	}
	return s.client.User.UpdateOneID(userID).SetWelcomeEmailSentAt(time.Now()).Exec(ctx)
}

func invitationAcceptanceTarget(companyID, userID int64) []predicate.User {
	return []predicate.User{
		user.IDEQ(userID),
		user.CompanyIDEQ(companyID),
		user.OnboardingCompletedAtIsNil(),
		user.IsActiveEQ(false),
	}
}

func validInvitationToken(hash string, now time.Time) []predicate.InvitationToken {
	return []predicate.InvitationToken{
		invitationtoken.TokenHashEQ(hash),
		invitationtoken.CompanyIDNotNil(),
		invitationtoken.ExpiresAtGT(now),
		invitationtoken.ConsumedAtIsNil(),
	}
}

func invalidateOpenInvites(ctx context.Context, client *ent.Client, companyID, userID int64, consumedAt time.Time) error {
	_, err := client.InvitationToken.Update().Where(
		invitationtoken.CompanyIDEQ(companyID),
		invitationtoken.UserIDEQ(userID),
		invitationtoken.ConsumedAtIsNil(),
	).SetConsumedAt(consumedAt).Save(ctx)
	if err != nil {
		return fmt.Errorf("invalidate invitations: %w", err)
	}
	return nil
}

func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func invitationTransactionConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "40001" || pgErr.Code == "40P01")
}

func createInvite(ctx context.Context, client *ent.Client, companyID, userID int64, now time.Time) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate invitation token: %w", err)
	}
	token := hex.EncodeToString(b)
	hash := sha256.Sum256([]byte(token))

	_, err := client.InvitationToken.Create().
		SetCompanyID(companyID).
		SetTokenHash(hex.EncodeToString(hash[:])).
		SetUserID(userID).
		SetCreatedAt(now).
		SetExpiresAt(now.Add(72 * time.Hour)).
		Save(ctx)
	if err != nil {
		return "", fmt.Errorf("create invitation token: %w", err)
	}
	return token, nil
}
