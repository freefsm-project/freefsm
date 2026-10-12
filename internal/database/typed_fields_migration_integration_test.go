package database

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestTypedFieldsUpgradeReviewIsNoncoercive(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	old := fstest.MapFS{}
	source := MigrationFS()
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() < "058_" {
			b, err := fs.ReadFile(source, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[e.Name()] = &fstest.MapFile{Data: b}
		}
	}
	if err = db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO custom_field_definitions(id,object_type,name,field_type,options) VALUES(900,'customer','Legacy value','mystery','[]')`); err != nil {
		t.Fatal(err)
	}
	err = db.Migrate(ctx, source)
	if err == nil || !strings.Contains(err.Error(), "900") || !strings.Contains(err.Error(), "review") {
		t.Fatalf("expected actionable legacy review, got %v", err)
	}
	var typ string
	if err = db.Pool.QueryRow(ctx, `SELECT field_type FROM custom_field_definitions WHERE id=900`).Scan(&typ); err != nil || typ != "mystery" {
		t.Fatalf("legacy definition changed: %q %v", typ, err)
	}
	var applied bool
	if err = db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='058_typed_custom_fields')`).Scan(&applied); err != nil || applied {
		t.Fatalf("failed migration was recorded: %v %v", applied, err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE custom_field_definitions SET field_type='select',options='["North"," north "]' WHERE id=900`); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx, source); err == nil || !strings.Contains(err.Error(), "ambiguous option") {
		t.Fatalf("ambiguous legacy options were accepted: %v", err)
	}
	// Explicit review corrects a known type; successful migration preserves its ID.
	if _, err = db.Pool.Exec(ctx, `UPDATE custom_field_definitions SET field_type='select',options='["Legacy option"]' WHERE id=900; INSERT INTO customers(company_id,display_name,custom_fields,deleted_at) SELECT id,'Archived legacy','[{"definition_id":900,"value":"Legacy option"}]',now() FROM companies LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	var before string
	if err = db.Pool.QueryRow(ctx, `SELECT custom_fields::text FROM customers WHERE display_name='Archived legacy'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	var after string
	if err = db.Pool.QueryRow(ctx, `SELECT custom_fields::text FROM customers WHERE display_name='Archived legacy'`).Scan(&after); err != nil || before != after {
		t.Fatalf("upgrade rewrote history: %s -> %s (%v)", before, after, err)
	}
}

func TestTypedFieldsLegacyPrecisionRequiresExplicitLosslessReview(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	old := fstest.MapFS{}
	source := MigrationFS()
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() < "058_" {
			b, err := fs.ReadFile(source, entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[entry.Name()] = &fstest.MapFile{Data: b}
		}
	}
	if err = db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO custom_field_definitions(id,object_type,name,field_type,options) VALUES(901,'customer','Legacy decimal','number','[]'); INSERT INTO customers(company_id,display_name,custom_fields) SELECT id,'Exact legacy','[{"definition_id":901,"value":"001.234"}]' FROM companies LIMIT 1`); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx, source); err == nil || !strings.Contains(err.Error(), "precision") {
		t.Fatalf("missing precision was guessed: %v", err)
	}
	if _, err = db.Pool.Exec(ctx, `ALTER TABLE custom_field_definitions ADD COLUMN precision integer; UPDATE custom_field_definitions SET precision=2 WHERE id=901`); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx, source); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("overprecision value was coerced: %v", err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE custom_field_definitions SET precision=3 WHERE id=901`); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	var value string
	if err = db.Pool.QueryRow(ctx, `SELECT custom_fields->0->>'value' FROM customers WHERE display_name='Exact legacy'`).Scan(&value); err != nil || value != "001.234" {
		t.Fatalf("recorded decimal changed: %q %v", value, err)
	}
}
