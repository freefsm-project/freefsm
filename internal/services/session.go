package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type SessionService struct {
	db *pgxpool.Pool
}

var (
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrInvalidSession is conclusive credential/account invalidity, never a failed
	// dependency lookup. Callers may discard credentials only for this error.
	ErrInvalidSession = errors.New("invalid session")
)

const (
	webSessionLifetime    = 7 * 24 * time.Hour
	mobileSessionLifetime = 30 * 24 * time.Hour
)

func NewSessionService(db *pgxpool.Pool) *SessionService {
	return &SessionService{db: db}
}

// CreateAuthenticated rechecks the password and account under the credential
// mutex. A login that verified an old password cannot publish after a reset.
func (s *SessionService) CreateAuthenticated(ctx context.Context, userID int64, password, kind, device string) (string, error) {
	token, hash, err := newSessionToken()
	if err != nil {
		return "", err
	}
	err = credentialTransaction(ctx, s.db, func(tx pgx.Tx) error {
		var stored string
		var forced bool
		if err := tx.QueryRow(ctx, `SELECT password_hash,force_password_change FROM users WHERE id=$1 AND is_active FOR UPDATE`, userID).Scan(&stored, &forced); err != nil {
			return ErrInvalidCredentials
		}
		if (kind != "web" && kind != "mobile") || (kind == "mobile" && forced) || bcrypt.CompareHashAndPassword([]byte(stored), []byte(password)) != nil {
			return ErrInvalidCredentials
		}
		lifetime := webSessionLifetime
		if kind == "mobile" {
			lifetime = mobileSessionLifetime
		}
		_, err := tx.Exec(ctx, `WITH instant AS (SELECT clock_timestamp() AS t) INSERT INTO sessions(token_hash,user_id,kind,created_at,expires_at,last_used_at,device_name) SELECT $1,$2,$3,t,t+$4*interval '1 second',t,nullif($5,'') FROM instant`, hash, userID, kind, int64(lifetime/time.Second), device)
		return err
	})
	if err != nil {
		return "", err
	}
	return token, nil
}

func (s *SessionService) Create(ctx context.Context, userID int64) (string, error) {
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return "", err
	}

	_, err = s.db.Exec(ctx,
		`WITH instant AS (SELECT clock_timestamp() AS t) INSERT INTO sessions (token_hash, user_id, created_at, expires_at, last_used_at, kind) SELECT $1,$2,t,t+interval '168 hours',t,'web' FROM instant`,
		tokenHash, userID,
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
	_, err = s.db.Exec(ctx, `
		WITH instant AS (SELECT clock_timestamp() AS t)
		INSERT INTO sessions (token_hash, user_id, created_at, expires_at, kind, last_used_at, device_name)
		SELECT $1,$2,t,t+interval '720 hours','mobile',t,NULLIF(BTRIM($3),'') FROM instant`,
		tokenHash, userID, deviceName,
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
		return 0, ErrInvalidSession
	}
	var userID int64
	err := s.db.QueryRow(ctx,
		`SELECT user_id FROM sessions s WHERE token_hash = $1 AND kind = 'web' AND revoked_at IS NULL AND expires_at > clock_timestamp() AND NOT EXISTS(SELECT 1 FROM session_upgrade_inventory i WHERE i.session_id=s.id AND i.disposition='review-required')`,
		hashSessionToken(token),
	).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrInvalidSession
	}
	if err != nil {
		return 0, fmt.Errorf("validate web session: %w", err)
	}
	return userID, nil
}

// ValidateMobile only authenticates. Failed action authorization must never
// extend validity; the API renews after successful online authorization.
func (s *SessionService) ValidateMobile(ctx context.Context, token string) (int64, error) {
	if token == "" {
		return 0, ErrInvalidSession
	}
	var userID int64
	err := s.db.QueryRow(ctx, `SELECT user_id FROM sessions s WHERE token_hash=$1 AND kind='mobile' AND revoked_at IS NULL AND expires_at>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM session_upgrade_inventory i WHERE i.session_id=s.id AND i.disposition='review-required')`, hashSessionToken(token)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrInvalidSession
	}
	if err != nil {
		return 0, fmt.Errorf("validate mobile session: %w", err)
	}
	return userID, nil
}

// RevokeMobile invalidates a mobile bearer session without deleting its audit metadata.
func (s *SessionService) RevokeMobile(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	err := credentialTransaction(ctx, s.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = COALESCE(revoked_at, NOW())
		WHERE token_hash = $1 AND kind = 'mobile' AND revoked_at IS NULL`, hashSessionToken(token))
		return err
	})
	return err
}

func (s *SessionService) Delete(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	return credentialTransaction(ctx, s.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE token_hash=$1 AND revoked_at IS NULL`, hashSessionToken(token))
		return err
	})
}

func hashSessionToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
