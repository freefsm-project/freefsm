package database

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
	"time"
)

func TestMobileSessionMigrationPreservesWebSessionsAndDropsMobileSessionsOnRollback(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL session migration tests")
	}
	ctx := context.Background()
	admin, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_session_migration_%d", time.Now().UnixNano())
	if _, err = admin.Pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Pool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`)
		admin.Close()
	})
	db, err := Connect(ctx, migrationSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if _, err = db.Pool.Exec(ctx, `
		CREATE TABLE users (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE sessions (
			id BIGSERIAL PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			expires_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);
		INSERT INTO users DEFAULT VALUES;
		INSERT INTO sessions(token_hash, user_id, expires_at, created_at)
		VALUES ('legacy', 1, NOW()+INTERVAL '1 day', NOW()-INTERVAL '2 days')`); err != nil {
		t.Fatal(err)
	}

	up, err := fs.ReadFile(MigrationFS(), "052_mobile_sessions.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	migrations := fstest.MapFS{"052_mobile_sessions.up.sql": &fstest.MapFile{Data: up}}
	if err := db.Migrate(ctx, migrations); err != nil {
		t.Fatalf("migrate legacy sessions: %v", err)
	}
	var kind string
	var createdAt, lastUsed time.Time
	var revokedAt *time.Time
	var deviceName *string
	if err := db.Pool.QueryRow(ctx, `SELECT kind, created_at, last_used_at, revoked_at, device_name FROM sessions WHERE token_hash='legacy'`).Scan(&kind, &createdAt, &lastUsed, &revokedAt, &deviceName); err != nil {
		t.Fatal(err)
	}
	if kind != "web" || !lastUsed.Equal(createdAt) || revokedAt != nil || deviceName != nil {
		t.Fatalf("legacy metadata = kind %q, created %v, last_used %v, revoked %v, device %v", kind, createdAt, lastUsed, revokedAt, deviceName)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,kind,created_at,last_used_at,expires_at) VALUES('uncertain-mobile',1,'mobile','2026-01-02T00:00:00Z','2026-01-01T00:00:00Z','2026-02-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	current, err := fs.ReadFile(MigrationFS(), "072_session_lifecycle.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, string(current)); err != nil {
		t.Fatal(err)
	}
	var unchanged bool
	if err := db.Pool.QueryRow(ctx, `SELECT i.recorded_kind=s.kind AND i.recorded_issue_time=s.created_at AND i.recorded_last_use_time=s.last_used_at AND i.recorded_expiry=s.expires_at AND i.disposition='review-required' AND s.revoked_at IS NULL FROM sessions s JOIN session_upgrade_inventory i ON i.session_id=s.id WHERE token_hash='uncertain-mobile'`).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("uncertain facts rewritten: %v %v", unchanged, err)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT recorded_issue_time=$1 AND recorded_last_use_time IS NULL AND recorded_kind IS NULL AND disposition='review-required' FROM session_upgrade_inventory WHERE session_id=1`, createdAt).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("legacy browser facts lost or invented: %v %v", unchanged, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE session_upgrade_inventory SET disposition='invented'`); err == nil {
		t.Fatal("upgrade evidence mutable")
	}

	if _, err = db.Pool.Exec(ctx, `
		INSERT INTO sessions(token_hash, user_id, expires_at, kind)
		VALUES
			('rollback-web', 1, NOW()+INTERVAL '1 day', 'web'),
			('rollback-mobile', 1, NOW()+INTERVAL '1 day', 'mobile')`); err != nil {
		t.Fatal(err)
	}
	down, err := fs.ReadFile(MigrationFS(), "052_mobile_sessions.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("roll back mobile sessions: %v", err)
	}

	var legacyValid, webValid, mobileValid bool
	if err := db.Pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM sessions WHERE token_hash='legacy' AND expires_at>NOW()),
		EXISTS(SELECT 1 FROM sessions WHERE token_hash='rollback-web' AND expires_at>NOW()),
		EXISTS(SELECT 1 FROM sessions WHERE token_hash='rollback-mobile' AND expires_at>NOW())`).Scan(&legacyValid, &webValid, &mobileValid); err != nil {
		t.Fatal(err)
	}
	if !legacyValid || !webValid || mobileValid {
		t.Fatalf("valid legacy sessions after rollback: legacy=%v web=%v mobile=%v", legacyValid, webValid, mobileValid)
	}
}
