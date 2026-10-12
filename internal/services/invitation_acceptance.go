package services

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// Acceptance is an intrinsic credential action. The invitation authenticates
// this one action, never product access or an Administrator's authority.
func (s *InvitationService) acceptPersistedInvitation(ctx context.Context, token, password string) (*ent.User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Read committed AFTER acquiring the shared account/Role/security fence.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		return nil, err
	}
	var id, company, role, invitation int64
	var name, email string
	err = tx.QueryRow(ctx, `SELECT u.id,u.company_id,coalesce(u.authorization_role_id,0),i.id,u.name,u.email FROM invitation_tokens i JOIN users u ON u.id=i.user_id AND u.company_id=i.company_id WHERE i.token_hash=$1 AND i.consumed_at IS NULL AND i.expires_at>clock_timestamp() AND NOT u.is_active AND u.onboarding_completed_at IS NULL FOR UPDATE OF u,i`, tokenHash(token)).Scan(&id, &company, &role, &invitation, &name, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidInvitation
	}
	if err != nil {
		return nil, err
	}
	policy := &ent.CompanySettings{}
	err = tx.QueryRow(ctx, `SELECT password_min_length,password_require_uppercase,password_require_lowercase,password_require_digit,password_require_special FROM company_settings WHERE company_id=$1 FOR SHARE`, company).Scan(&policy.PasswordMinLength, &policy.PasswordRequireUppercase, &policy.PasswordRequireLowercase, &policy.PasswordRequireDigit, &policy.PasswordRequireSpecial)
	if err != nil {
		return nil, fmt.Errorf("read invitation password policy: %w", err)
	}
	if err := NewUserService(s.client).ValidatePassword(password, policy); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvitationPassword, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE invitation_tokens SET consumed_at=clock_timestamp() WHERE id=$1`, invitation); err != nil {
		return nil, err
	}
	var completed time.Time
	if err := tx.QueryRow(ctx, `UPDATE users SET password_hash=$2,is_active=true,onboarding_completed_at=clock_timestamp(),force_password_change=false WHERE id=$1 RETURNING onboarding_completed_at`, id, string(hash)).Scan(&completed); err != nil {
		return nil, err
	}
	if err := protectedaudit.RecordProduct(ctx, tx, protectedaudit.ProductAction{ActorID: id, RoleID: role, Action: "user.invitation.accept", Scope: "O", TargetType: "user", TargetID: id, Channel: "web", Detail: map[string]any{"company_id": company}}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &ent.User{ID: id, CompanyID: &company, AuthorizationRoleID: &role, Name: name, Email: email, PasswordHash: string(hash), IsActive: true, OnboardingCompletedAt: &completed}, nil
}
