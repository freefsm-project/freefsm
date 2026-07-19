package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type SessionService struct {
	db *pgxpool.Pool
}

const (
	webSessionLifetime       = 7 * 24 * time.Hour
	mobileSessionLifetime    = 30 * 24 * time.Hour
	mobileSessionRefreshRate = 5 * time.Minute
)

func NewSessionService(db *pgxpool.Pool) *SessionService {
	return &SessionService{db: db}
}

func (s *SessionService) Create(ctx context.Context, userID int64) (string, error) {
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return "", err
	}

	_, err = s.db.Exec(ctx,
		`INSERT INTO sessions (token_hash, user_id, expires_at, kind) VALUES ($1, $2, $3, 'web')`,
		tokenHash, userID, time.Now().Add(webSessionLifetime),
	)
	if err != nil {
		return "", fmt.Errorf("save session: %w", err)
	}
	return token, nil
}

// CreateMobile creates a bearer session whose expiry slides with mobile use.
func (s *SessionService) CreateMobile(ctx context.Context, userID int64, deviceName string) (string, error) {
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return "", err
	}
	now := time.Now()

	_, err = s.db.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at, kind, last_used_at, device_name)
		VALUES ($1, $2, $3, 'mobile', $4, NULLIF(BTRIM($5), ''))`,
		tokenHash, userID, now.Add(mobileSessionLifetime), now, deviceName,
	)
	if err != nil {
		return "", fmt.Errorf("save mobile session: %w", err)
	}
	return token, nil
}

func newSessionToken() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(b)
	return token, hashSessionToken(token), nil
}

func (s *SessionService) Validate(ctx context.Context, token string) (int64, error) {
	if token == "" {
		return 0, fmt.Errorf("empty token")
	}
	var userID int64
	err := s.db.QueryRow(ctx,
		`SELECT user_id FROM sessions WHERE token_hash = $1 AND kind = 'web' AND expires_at > NOW()`,
		hashSessionToken(token),
	).Scan(&userID)
	if err != nil {
		return 0, fmt.Errorf("invalid session")
	}
	return userID, nil
}

// ValidateMobile validates a non-revoked mobile bearer session and periodically
// advances its 30-day inactivity deadline. Refresh writes are limited to once
// per refresh interval per session.
func (s *SessionService) ValidateMobile(ctx context.Context, token string) (int64, error) {
	if token == "" {
		return 0, fmt.Errorf("empty token")
	}
	var userID int64
	err := s.db.QueryRow(ctx, `
		WITH refreshed AS (
			UPDATE sessions
			SET last_used_at = NOW(), expires_at = NOW() + $2 * INTERVAL '1 second'
			WHERE token_hash = $1
			  AND kind = 'mobile'
			  AND revoked_at IS NULL
			  AND expires_at > NOW()
			  AND last_used_at <= NOW() - $3 * INTERVAL '1 second'
			RETURNING user_id
		)
		SELECT user_id FROM refreshed
		UNION ALL
		SELECT user_id FROM sessions
		WHERE token_hash = $1
		  AND kind = 'mobile'
		  AND revoked_at IS NULL
		  AND expires_at > NOW()
		  AND NOT EXISTS (SELECT 1 FROM refreshed)
		LIMIT 1`,
		hashSessionToken(token),
		int64(mobileSessionLifetime/time.Second),
		int64(mobileSessionRefreshRate/time.Second),
	).Scan(&userID)
	if err != nil {
		return 0, fmt.Errorf("invalid mobile session")
	}
	return userID, nil
}

// RevokeMobile invalidates a mobile bearer session without deleting its audit metadata.
func (s *SessionService) RevokeMobile(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = COALESCE(revoked_at, NOW())
		WHERE token_hash = $1 AND kind = 'mobile' AND revoked_at IS NULL`, hashSessionToken(token))
	return err
}

func (s *SessionService) Delete(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, hashSessionToken(token))
	return err
}

func hashSessionToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
