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

func TestActiveTimeEntryMigration053ReconcilesDuplicatesAndEnforcesInvariant(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL migration tests")
	}
	db, ctx := activeTimeEntryMigrationDatabaseThrough052(t, dsn)

	var companyID, userID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Time entry migration',$1) RETURNING id`, fmt.Sprintf("time-entry-migration-%d", time.Now().UnixNano())), &companyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,$2,'x','Time Entry Migration','tech') RETURNING id`, companyID, fmt.Sprintf("time-entry-migration-%d@example.test", time.Now().UnixNano())), &userID)

	clockIns := []time.Time{
		time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC),
	}
	entryIDs := make([]int64, len(clockIns))
	for i, clockIn := range clockIns {
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in,notes) VALUES($1,$2,$3,$4) RETURNING id`, companyID, userID, clockIn, fmt.Sprintf("entry-%d", i)), &entryIDs[i])
	}

	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatalf("migrate 053: %v", err)
	}

	wantClockOuts := []*time.Time{&clockIns[1], &clockIns[2], &clockIns[3], nil}
	for i, entryID := range entryIDs {
		var clockIn time.Time
		var clockOut *time.Time
		var notes string
		if err := db.Pool.QueryRow(ctx, `SELECT clock_in,clock_out,notes FROM time_entries WHERE id=$1`, entryID).Scan(&clockIn, &clockOut, &notes); err != nil {
			t.Fatalf("entry %d was not preserved: %v", i, err)
		}
		if !clockIn.Equal(clockIns[i]) || notes != fmt.Sprintf("entry-%d", i) {
			t.Errorf("entry %d changed: clock_in=%v notes=%q", i, clockIn, notes)
		}
		if wantClockOuts[i] == nil {
			if clockOut != nil {
				t.Errorf("entry %d clock_out=%v, want active", i, clockOut)
			}
		} else if clockOut == nil || !clockOut.Equal(*wantClockOuts[i]) {
			t.Errorf("entry %d clock_out=%v, want %v", i, clockOut, *wantClockOuts[i])
		}
	}

	if _, err := db.Pool.Exec(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in) VALUES($1,$2,$3)`, companyID, userID, clockIns[3].Add(time.Hour)); err == nil {
		t.Fatal("unique index accepted a second active time entry")
	}

	down, err := fs.ReadFile(MigrationFS(), "053_active_time_entry_invariant.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down 053: %v", err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in) VALUES($1,$2,$3)`, companyID, userID, clockIns[3].Add(time.Hour)); err != nil {
		t.Fatalf("second active time entry after down migration: %v", err)
	}
}

func activeTimeEntryMigrationDatabaseThrough052(t *testing.T, dsn string) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	admin, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_active_time_entry_migration_%d", time.Now().UnixNano())
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

	through052 := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "053_" {
			continue
		}
		data, readErr := fs.ReadFile(MigrationFS(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		through052[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err = db.Migrate(ctx, through052); err != nil {
		t.Fatalf("migrate through 052: %v", err)
	}
	return db, ctx
}
