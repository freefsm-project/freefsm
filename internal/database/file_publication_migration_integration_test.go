package database

import (
	"os"
	"testing"
)

func TestFilePublicationUpgradePreservesRecordedFacts(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	a := seedFileOwnershipTenant(t, ctx, db, "publication-history")
	insertMigrationFile(t, ctx, db, a.company, a.user, "customer", a.customer)
	var before, after []byte
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(f) FROM files f`), &before)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(f)-'revision'-'removed_at'-'removed_by'-'sha256' FROM files f`), &after)
	if string(before) != string(after) {
		t.Fatalf("migration rewrote history: %s -> %s", before, after)
	}
	var honest bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT revision=1 AND removed_at IS NULL AND removed_by IS NULL AND sha256 IS NULL FROM files`), &honest)
	if !honest {
		t.Fatal("migration invented validation or removal history")
	}
}
