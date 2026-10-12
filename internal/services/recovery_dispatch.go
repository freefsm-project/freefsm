package services

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Request accepts every address using the same durable transaction. Binding the
// queued intent to the current account revision prevents delayed work from
// reviving recovery after a password, disablement, or email-identity decision.
func (s *PasswordResetService) Request(ctx context.Context, email, baseURL string) error {
	return credentialTransaction(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO password_recovery_requests(email,base_url,user_id,credential_revision)
   SELECT $1,$2,u.id,u.credential_revision FROM (SELECT 1) request
   LEFT JOIN users u ON u.email=$1 AND u.is_active`, strings.TrimSpace(email), baseURL)
		return err
	})
}

// DispatchOne resumes abandoned claims after two minutes. SMTP acceptance is
// external and may be repeated after a lost acknowledgement; each attempt issues
// a new newest-only link, and no raw bearer token is persisted in the queue.
func (s *PasswordResetService) DispatchOne(ctx context.Context, email *EmailService) (bool, error) {
	return s.dispatchOne(ctx, email, nil)
}

// Refresh restored connection metadata only after admission, before any settings
// or claim query, and keep that same admission through the entire attempt.
func (s *PasswordResetService) dispatchOne(ctx context.Context, email *EmailService, beforeWork func() error) (bool, error) {
	ctx, release, err := email.control.EnterEmail(ctx)
	if err != nil {
		return false, err
	}
	defer release()
	if beforeWork != nil {
		if err = beforeWork(); err != nil {
			return false, err
		}
	}
	if err = email.ValidateConfiguration(ctx); err != nil {
		return false, err
	}
	var id int64
	var userID, revision *int64
	var recipient, base string
	err = s.pool.QueryRow(ctx, `UPDATE password_recovery_requests SET claimed_until=clock_timestamp()+interval '2 minutes'
  WHERE id=(SELECT id FROM password_recovery_requests WHERE completed_at IS NULL AND (claimed_until IS NULL OR claimed_until<clock_timestamp()) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1)
  RETURNING id,email,base_url,user_id,credential_revision`).Scan(&id, &recipient, &base, &userID, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if userID != nil {
		token, issueErr := s.createToken(ctx, *userID, []string{recipient}, revision)
		if issueErr != nil && !errors.Is(issueErr, ErrInvalidRecovery) {
			return true, issueErr
		}
		if issueErr == nil {
			// Identity changes after issuance invalidate the proof at the database
			// boundary, so even an already-in-flight old-mailbox message grants nothing.
			if err = email.SendPasswordReset(ctx, recipient, "", base+"?token="+url.QueryEscape(token)); err != nil {
				slog.Warn("recovery email transport failed; a new request may be submitted")
			}
		}
	}
	_, err = s.pool.Exec(ctx, `UPDATE password_recovery_requests SET completed_at=clock_timestamp(),email='',base_url='' WHERE id=$1`, id)
	return true, err
}

func (s *PasswordResetService) RunDispatch(ctx context.Context, email *EmailService, beforeWork func() error) error {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
		workCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := s.dispatchOne(workCtx, email, beforeWork)
		cancel()
		if err != nil && ctx.Err() == nil {
			slog.Warn("recovery dispatch deferred")
		}
	}
}
