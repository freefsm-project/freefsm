package database

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAuthorizationPreflightPreservesLegacyFactsWithoutInferringGrants(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL preflight tests")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	const secret = "recognizable-password-hash-never-export"
	_, err := db.Pool.Exec(ctx, `INSERT INTO users(id,email,password_hash,name,role,is_active)
		VALUES (901,'one@example.test',$1,'Dispatcher','admin',true),
		       (902,'two@example.test',$1,'Administrator','tech',false)`, secret)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err := db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM users u`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	report, err := db.AuthorizationPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !report.ReviewRequired || len(report.Users) != 2 || len(report.LegacyRoles) != 2 {
		t.Fatalf("expected unresolved existing identities: %+v", report)
	}
	if report.Users[0].ID != 901 || report.Users[0].LegacyRole != "admin" || report.Users[0].Name != "Dispatcher" || !report.Users[0].Enabled || !report.Users[0].PasswordPresent {
		t.Fatalf("recorded identity changed: %+v", report.Users[0])
	}
	if report.Users[1].ID != 902 || report.Users[1].LegacyRole != "tech" || report.Users[1].Enabled {
		t.Fatalf("recorded identity changed: %+v", report.Users[1])
	}
	if report.RecordedGrantStorage != "absent_in_supported_legacy_schema" {
		t.Fatalf("must distinguish absent grant history from a recorded empty grant set: %s", report.RecordedGrantStorage)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "password_hash") {
		t.Fatal("report contains credential material")
	}
	again, err := db.AuthorizationPreflight(ctx)
	if err != nil || !reflect.DeepEqual(report, again) {
		t.Fatalf("unchanged input must produce identical review inventory: err=%v", err)
	}
	var after string
	if err := db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(u) ORDER BY id)::text FROM users u`).Scan(&after); err != nil || before != after {
		t.Fatalf("preflight changed existing users: err=%v", err)
	}
}

func TestAuthorizationPreflightStopsOnUnrecognizedAuthorityStorage(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `ALTER TABLE users ADD COLUMN permission_overrides jsonb NOT NULL DEFAULT '{}'`); err != nil {
		t.Fatal(err)
	}
	report, err := db.AuthorizationPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !report.ReviewRequired || report.RecordedGrantStorage != "unknown" || !strings.Contains(strings.Join(report.ReviewReasons, " "), "permission_overrides") {
		t.Fatalf("unrecognized recorded authority was silently discarded: %+v", report)
	}
}

func TestAuthorizationPreflightUsesOneSnapshotAcrossConcurrentChanges(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(email,password_hash,name,role) VALUES ('snapshot@example.test','x','Before','tech')`); err != nil {
		t.Fatal(err)
	}
	writer, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	if _, err := writer.Exec(ctx, `LOCK TABLE users IN ACCESS EXCLUSIVE MODE;
		UPDATE users SET name='After';
		INSERT INTO schema_migrations(name) VALUES ('999_concurrent_review_marker')`); err != nil {
		t.Fatal(err)
	}
	var writerPID int
	if err := writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&writerPID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		report AuthorizationInventory
		err    error
	}
	readerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	done := make(chan result, 1)
	go func() {
		r, err := db.AuthorizationPreflight(readerCtx)
		done <- result{r, err}
	}()
	// Wait for the inventory to reach the locked users table. This proves its
	// schema/migration snapshot exists before the competing transaction commits.
	for {
		var waiting bool
		err := db.Pool.QueryRow(readerCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, writerPID).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.err != nil || len(got.report.Users) != 1 || got.report.Users[0].Name != "Before" || strings.Contains(strings.Join(got.report.Migrations, " "), "999_concurrent") {
		t.Fatalf("mixed preflight snapshot: %+v err=%v", got.report, got.err)
	}
	after, err := db.AuthorizationPreflight(ctx)
	if err != nil || after.Users[0].Name != "After" || !strings.Contains(strings.Join(after.Migrations, " "), "999_concurrent") || after.Fingerprint == got.report.Fingerprint {
		t.Fatalf("new inventory did not observe committed change: %+v err=%v", after, err)
	}
}
