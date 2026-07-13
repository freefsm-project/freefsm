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

func TestOnboardingCompletionMigration048BackfillIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := onboardingMigrationDatabaseThrough047(t, dsn)

	if _, err := db.Pool.Exec(ctx, `ALTER TABLE users ADD COLUMN onboarding_completed_at TIMESTAMPTZ`); err != nil {
		t.Fatal(err)
	}
	var companyID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Onboarding migration',$1) RETURNING id`, fmt.Sprintf("onboarding-%d", time.Now().UnixNano())), &companyID)

	createdAt := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	welcomeAt := createdAt.Add(30 * time.Minute)
	firstInviteAt := createdAt.Add(45 * time.Minute)
	resentInviteAt := createdAt.Add(90 * time.Minute)
	consumedAt := createdAt.Add(time.Hour)
	sessionAt := createdAt.Add(2 * time.Hour)
	loginAt := createdAt.Add(3 * time.Hour)
	preservedAt := createdAt.Add(4 * time.Hour)

	active := seedOnboardingMigrationUser(t, ctx, db, companyID, "active", true, &welcomeAt, createdAt)
	legacyResent := seedOnboardingMigrationUser(t, ctx, db, companyID, "legacy-resent", false, &welcomeAt, createdAt)
	acceptedDisabled := seedOnboardingMigrationUser(t, ctx, db, companyID, "accepted-disabled", false, &welcomeAt, createdAt)
	loggedInDisabled := seedOnboardingMigrationUser(t, ctx, db, companyID, "logged-in-disabled", false, &welcomeAt, createdAt)
	sessionDisabled := seedOnboardingMigrationUser(t, ctx, db, companyID, "session-disabled", false, &welcomeAt, createdAt)
	directInactive := seedOnboardingMigrationUser(t, ctx, db, companyID, "direct-inactive", false, nil, createdAt)
	pending := seedOnboardingMigrationUser(t, ctx, db, companyID, "pending", false, &welcomeAt, createdAt)
	orderedEvidence := seedOnboardingMigrationUser(t, ctx, db, companyID, "ordered-evidence", false, &welcomeAt, createdAt)
	preserved := seedOnboardingMigrationUser(t, ctx, db, companyID, "preserved", true, nil, createdAt)

	seedOnboardingMigrationInvitation(t, ctx, db, active, nil, "active", firstInviteAt)
	seedOnboardingMigrationInvitation(t, ctx, db, legacyResent, &consumedAt, "legacy-consumed", firstInviteAt)
	seedOnboardingMigrationInvitation(t, ctx, db, legacyResent, nil, "legacy-open", resentInviteAt)
	seedOnboardingMigrationInvitation(t, ctx, db, acceptedDisabled, &consumedAt, "accepted", firstInviteAt)
	seedOnboardingMigrationInvitation(t, ctx, db, loggedInDisabled, nil, "logged-in", firstInviteAt)
	seedOnboardingMigrationInvitation(t, ctx, db, sessionDisabled, nil, "session", firstInviteAt)
	seedOnboardingMigrationInvitation(t, ctx, db, directInactive, nil, "direct", firstInviteAt)
	seedOnboardingMigrationInvitation(t, ctx, db, pending, nil, "pending", firstInviteAt)
	seedOnboardingMigrationInvitation(t, ctx, db, orderedEvidence, &consumedAt, "ordered-consumed", firstInviteAt)
	seedOnboardingMigrationInvitation(t, ctx, db, orderedEvidence, nil, "ordered-open", resentInviteAt)

	seedOnboardingMigrationLogin(t, ctx, db, companyID, loggedInDisabled, loginAt)
	seedOnboardingMigrationSession(t, ctx, db, sessionDisabled, sessionAt, "retained-session")
	seedOnboardingMigrationLogin(t, ctx, db, companyID, orderedEvidence, loginAt)
	seedOnboardingMigrationSession(t, ctx, db, orderedEvidence, sessionAt, "ordered-session")
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET onboarding_completed_at=$1 WHERE id=$2`, preservedAt, preserved); err != nil {
		t.Fatal(err)
	}

	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}

	expected := map[int64]*time.Time{
		active:           &createdAt,
		legacyResent:     nil,
		acceptedDisabled: &consumedAt,
		loggedInDisabled: &loginAt,
		sessionDisabled:  &sessionAt,
		directInactive:   &createdAt,
		pending:          nil,
		orderedEvidence:  &consumedAt,
		preserved:        &preservedAt,
	}
	assertOnboardingCompletionTimes(t, ctx, db, expected)
	assertOnboardingInvitationCompanies(t, ctx, db, companyID)

	up, err := fs.ReadFile(MigrationFS(), "048_onboarding_completion.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}
	assertOnboardingCompletionTimes(t, ctx, db, expected)
	assertOnboardingInvitationCompanies(t, ctx, db, companyID)
}

func TestOnboardingCompletionMigration048DownRemovesOnlyColumnIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := onboardingMigrationDatabaseThrough047(t, dsn)
	var companyID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Onboarding down',$1) RETURNING id`, fmt.Sprintf("onboarding-down-%d", time.Now().UnixNano())), &companyID)
	userID := seedOnboardingMigrationUser(t, ctx, db, companyID, "down", false, nil, time.Now())
	seedOnboardingMigrationInvitation(t, ctx, db, userID, nil, "down", time.Now())

	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	down, err := fs.ReadFile(MigrationFS(), "048_onboarding_completion.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}

	var columnExists bool
	var tokenCompanyID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='users' AND column_name='onboarding_completed_at')`), &columnExists)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM invitation_tokens WHERE user_id=$1`, userID), &tokenCompanyID)
	if columnExists || tokenCompanyID != companyID {
		t.Fatalf("column exists=%v token company=%d want %d", columnExists, tokenCompanyID, companyID)
	}
}

func seedOnboardingMigrationUser(t *testing.T, ctx context.Context, db *DB, companyID int64, name string, active bool, welcomeAt *time.Time, createdAt time.Time) int64 {
	t.Helper()
	var userID int64
	email := fmt.Sprintf("onboarding-%s-%d@example.test", name, time.Now().UnixNano())
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role,is_active,welcome_email_sent_at,created_at,updated_at) VALUES($1,$2,'x',$3,'tech',$4,$5,$6,$6) RETURNING id`, companyID, email, name, active, welcomeAt, createdAt), &userID)
	return userID
}

func seedOnboardingMigrationInvitation(t *testing.T, ctx context.Context, db *DB, userID int64, consumedAt *time.Time, token string, createdAt time.Time) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO invitation_tokens(token_hash,user_id,expires_at,consumed_at,created_at) VALUES($1,$2,$3,$4,$5)`, fmt.Sprintf("%s-%d", token, userID), userID, createdAt.Add(24*time.Hour), consumedAt, createdAt); err != nil {
		t.Fatal(err)
	}
}

func seedOnboardingMigrationLogin(t *testing.T, ctx context.Context, db *DB, companyID, userID int64, createdAt time.Time) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,created_at) VALUES($1,$2,'logged_in','user',$2,$3)`, companyID, userID, createdAt); err != nil {
		t.Fatal(err)
	}
}

func seedOnboardingMigrationSession(t *testing.T, ctx context.Context, db *DB, userID int64, createdAt time.Time, token string) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at,created_at) VALUES($1,$2,$3,$4)`, token, userID, createdAt.Add(-time.Hour), createdAt); err != nil {
		t.Fatal(err)
	}
}

func assertOnboardingCompletionTimes(t *testing.T, ctx context.Context, db *DB, expected map[int64]*time.Time) {
	t.Helper()
	for userID, want := range expected {
		var got *time.Time
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT onboarding_completed_at FROM users WHERE id=$1`, userID), &got)
		if want == nil {
			if got != nil {
				t.Fatalf("user %d completion=%s want NULL", userID, got)
			}
			continue
		}
		if got == nil || !got.Equal(*want) {
			t.Fatalf("user %d completion=%v want %s", userID, got, want)
		}
	}
}

func assertOnboardingInvitationCompanies(t *testing.T, ctx context.Context, db *DB, companyID int64) {
	t.Helper()
	var mismatches int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM invitation_tokens i JOIN users u ON u.id=i.user_id WHERE u.company_id=$1 AND i.company_id IS DISTINCT FROM u.company_id`, companyID), &mismatches)
	if mismatches != 0 {
		t.Fatalf("invitation company mismatches=%d", mismatches)
	}
}

func onboardingMigrationDatabaseThrough047(t *testing.T, dsn string) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	admin, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_onboarding_migration_%d", time.Now().UnixNano())
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

	through047 := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "048_" {
			continue
		}
		data, readErr := fs.ReadFile(MigrationFS(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		through047[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err = db.Migrate(ctx, through047); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}
