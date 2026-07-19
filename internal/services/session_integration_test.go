package services

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSessionServiceSeparatesWebAndMobileSessions(t *testing.T) {
	pool, userID := openSessionTestPool(t)
	svc := NewSessionService(pool)
	ctx := context.Background()

	webToken, err := svc.Create(ctx, userID)
	if err != nil {
		t.Fatalf("create web session: %v", err)
	}
	if got, err := svc.Validate(ctx, webToken); err != nil || got != userID {
		t.Fatalf("validate web session = (%d, %v), want (%d, nil)", got, err, userID)
	}
	if _, err := svc.ValidateMobile(ctx, webToken); err == nil {
		t.Fatal("web session authenticated as a mobile session")
	}

	mobileToken, err := svc.CreateMobile(ctx, userID, "  Adam's phone  ")
	if err != nil {
		t.Fatalf("create mobile session: %v", err)
	}
	if _, err := svc.Validate(ctx, mobileToken); err == nil {
		t.Fatal("mobile session authenticated as a web session")
	}
	if got, err := svc.ValidateMobile(ctx, mobileToken); err != nil || got != userID {
		t.Fatalf("validate mobile session = (%d, %v), want (%d, nil)", got, err, userID)
	}

	var kind, deviceName string
	var lifetimeSeconds int64
	if err := pool.QueryRow(ctx, `SELECT kind, device_name, EXTRACT(EPOCH FROM expires_at-created_at)::bigint FROM sessions WHERE token_hash=$1`, hashSessionToken(mobileToken)).Scan(&kind, &deviceName, &lifetimeSeconds); err != nil {
		t.Fatal(err)
	}
	if kind != "mobile" || deviceName != "Adam's phone" {
		t.Fatalf("mobile metadata = (%q, %q)", kind, deviceName)
	}
	lifetime := time.Duration(lifetimeSeconds) * time.Second
	if lifetime < mobileSessionLifetime-time.Minute || lifetime > mobileSessionLifetime+time.Minute {
		t.Fatalf("mobile lifetime = %v, want about %v", lifetime, mobileSessionLifetime)
	}

	if err := svc.Delete(ctx, webToken); err != nil {
		t.Fatalf("delete web session: %v", err)
	}
	if _, err := svc.Validate(ctx, webToken); err == nil {
		t.Fatal("deleted web session remained valid")
	}
}

func TestSessionServiceMobileSlidingExpiryAndRevocation(t *testing.T) {
	pool, userID := openSessionTestPool(t)
	svc := NewSessionService(pool)
	ctx := context.Background()
	token, err := svc.CreateMobile(ctx, userID, "tablet")
	if err != nil {
		t.Fatal(err)
	}
	hash := hashSessionToken(token)

	oldUse := time.Now().Add(-10 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET last_used_at=$2, expires_at=NOW()+INTERVAL '1 hour' WHERE token_hash=$1`, hash, oldUse); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ValidateMobile(ctx, token); err != nil {
		t.Fatalf("validate and refresh mobile session: %v", err)
	}
	var refreshedUse, refreshedExpiry time.Time
	if err := pool.QueryRow(ctx, `SELECT last_used_at, expires_at FROM sessions WHERE token_hash=$1`, hash).Scan(&refreshedUse, &refreshedExpiry); err != nil {
		t.Fatal(err)
	}
	if !refreshedUse.After(oldUse) || time.Until(refreshedExpiry) < mobileSessionLifetime-time.Minute {
		t.Fatalf("session was not refreshed: last_used=%v expires=%v", refreshedUse, refreshedExpiry)
	}

	if _, err := svc.ValidateMobile(ctx, token); err != nil {
		t.Fatalf("validate within refresh throttle: %v", err)
	}
	var throttledUse, throttledExpiry time.Time
	if err := pool.QueryRow(ctx, `SELECT last_used_at, expires_at FROM sessions WHERE token_hash=$1`, hash).Scan(&throttledUse, &throttledExpiry); err != nil {
		t.Fatal(err)
	}
	if !throttledUse.Equal(refreshedUse) || !throttledExpiry.Equal(refreshedExpiry) {
		t.Fatal("validation inside the refresh interval wrote session timestamps")
	}

	if err := svc.RevokeMobile(ctx, token); err != nil {
		t.Fatalf("revoke mobile session: %v", err)
	}
	if err := svc.RevokeMobile(ctx, token); err != nil {
		t.Fatalf("repeat revocation: %v", err)
	}
	var revokedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT revoked_at FROM sessions WHERE token_hash=$1`, hash).Scan(&revokedAt); err != nil || revokedAt == nil {
		t.Fatalf("revoked_at = %v, err = %v", revokedAt, err)
	}
	if _, err := svc.ValidateMobile(ctx, token); err == nil {
		t.Fatal("revoked mobile session remained valid")
	}

	expiredToken, err := svc.CreateMobile(ctx, userID, "old phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET expires_at=NOW()-INTERVAL '1 second' WHERE token_hash=$1`, hashSessionToken(expiredToken)); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ValidateMobile(ctx, expiredToken); err == nil {
		t.Fatal("expired mobile session remained valid")
	}
}

func openSessionTestPool(t *testing.T) (*pgxpool.Pool, int64) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL session tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_session_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		admin.Close()
	})

	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `
		CREATE TABLE users (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE sessions (
			id BIGSERIAL PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			kind TEXT NOT NULL DEFAULT 'web' CHECK (kind IN ('web', 'mobile')),
			last_used_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			revoked_at TIMESTAMPTZ,
			device_name TEXT
		)`); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err = pool.QueryRow(ctx, `INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return pool, userID
}
