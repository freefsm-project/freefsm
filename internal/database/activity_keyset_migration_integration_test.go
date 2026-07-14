package database

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func TestActivityKeysetMigration049CatalogUpDownIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL migration tests")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)

	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatalf("migrate 049: %v", err)
	}
	assertActivityIndexCatalog(t, ctx, db, true)
	var applied bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='049_activity_keyset_indexes')`).Scan(&applied); err != nil || !applied {
		t.Fatalf("migration catalog applied=%v err=%v", applied, err)
	}

	down, err := fs.ReadFile(MigrationFS(), "049_activity_keyset_indexes.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down 049: %v", err)
	}
	assertActivityIndexCatalog(t, ctx, db, false)

	var legacyObject, legacyCreated bool
	if err := db.Pool.QueryRow(ctx, `SELECT
		to_regclass(current_schema() || '.idx_activity_object') IS NOT NULL,
		to_regclass(current_schema() || '.idx_activity_created') IS NOT NULL`).Scan(&legacyObject, &legacyCreated); err != nil {
		t.Fatal(err)
	}
	if !legacyObject || !legacyCreated {
		t.Fatalf("legacy indexes retained: object=%v created=%v", legacyObject, legacyCreated)
	}
}

func assertActivityIndexCatalog(t *testing.T, ctx context.Context, db *DB, exists bool) {
	t.Helper()
	want := map[string]string{
		"activity_logs_company_created_id_idx":        "company_id, created_at DESC, id DESC",
		"activity_logs_company_type_created_id_idx":   "company_id, object_type, created_at DESC, id DESC",
		"activity_logs_company_object_created_id_idx": "company_id, object_type, object_id, created_at DESC, id DESC",
	}
	for name, columns := range want {
		var definition string
		if err := db.Pool.QueryRow(ctx, `SELECT coalesce((SELECT indexdef FROM pg_indexes WHERE schemaname=current_schema() AND tablename='activity_logs' AND indexname=$1),'')`, name).Scan(&definition); err != nil {
			t.Fatalf("index %s: %v", name, err)
		}
		if !exists && definition != "" {
			t.Fatalf("index %s still exists", name)
		}
		if exists && !strings.Contains(definition, "("+columns+")") {
			t.Fatalf("index %s definition = %v, want columns %q", name, definition, columns)
		}
	}
}

func activityMigrationDatabaseThrough048(t *testing.T, dsn string) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	admin, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_activity_migration_%d", time.Now().UnixNano())
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

	through048 := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "049_" {
			continue
		}
		data, readErr := fs.ReadFile(MigrationFS(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		through048[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err = db.Migrate(ctx, through048); err != nil {
		t.Fatalf("migrate through 048: %v", err)
	}
	return db, ctx
}
