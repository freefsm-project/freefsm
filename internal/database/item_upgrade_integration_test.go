package database

import (
	"context"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
)

func TestItemUpgradePreservesIdentitiesSnapshotsAndRejectsInvalidLegacy(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() < "058" {
			b, err := fs.ReadFile(MigrationFS(), e.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[e.Name()] = &fstest.MapFile{Data: b}
		}
	}
	if err := db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, `INSERT INTO items(id,name,type,sku,unit_price,unit_cost,tax_rate,deleted_at) VALUES(4301,'Legacy','product','LEGACY-43',41.25,19.95,'invalid-tax',now());
 INSERT INTO customers(id,display_name) VALUES(4301,'Legacy Customer');
 INSERT INTO jobs(id,customer_id,job_type,line_items) VALUES(4301,4301,'Legacy Job','[{"item_id":4301,"title":"Captured","unit_price":12.34,"quantity":2}]');
 INSERT INTO authorization_roles(name) VALUES('Existing custom role');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, MigrationFS()); err == nil || !strings.Contains(err.Error(), "4301") {
		t.Fatalf("invalid legacy tax must require explicit ID-based resolution: %v", err)
	}
	var cost, sku string
	if err := db.Pool.QueryRow(ctx, `SELECT unit_cost::text,sku FROM items WHERE id=4301`).Scan(&cost, &sku); err != nil || cost != "19.95" || sku != "LEGACY-43" {
		t.Fatalf("failed upgrade lost data: %s %s %v", cost, sku, err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE items SET tax_rate='7.5' WHERE id=4301`); err != nil {
		t.Fatal(err)
	}
	var before string
	items := legacyColumnProjection(t, ctx, db, "items")
	jobs := legacyColumnProjection(t, ctx, db, "jobs")
	grants := legacyColumnProjection(t, ctx, db, "authorization_grants")
	snapshot := `SELECT jsonb_build_object('item',(SELECT to_jsonb(i) FROM (SELECT ` + items + ` FROM items WHERE id=4301) i),'job',(SELECT to_jsonb(j) FROM (SELECT ` + jobs + ` FROM jobs WHERE id=4301) j),'grants',(SELECT coalesce(jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope),'[]') FROM (SELECT ` + grants + ` FROM authorization_grants) g))::text`
	if err := db.Pool.QueryRow(ctx, snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := db.Pool.QueryRow(ctx, snapshot).Scan(&after); err != nil || before != after {
		t.Fatalf("upgrade rewrote recorded facts: %v\nbefore %s\nafter %s", err, before, after)
	}
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO items(name,type) VALUES('Legacy','service')`); err != nil {
		t.Fatalf("duplicate names must be allowed: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE items SET sku='CHANGED-43' WHERE id=4301`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO items(name,sku) VALUES('Another','LEGACY-43')`); err == nil {
		t.Fatal("old SKU was reused")
	}
}

// legacyColumnProjection freezes every pre-upgrade column for both snapshots.
// Explicit SQL identifiers allow additive columns but fail on dropped/renamed
// columns, including nullable ones whose loss JSON key lookups could conceal.
func legacyColumnProjection(t *testing.T, ctx context.Context, db *DB, table string) string {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `SELECT * FROM `+pgx.Identifier{table}.Sanitize()+` LIMIT 0`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var columns []string
	for _, field := range rows.FieldDescriptions() {
		columns = append(columns, pgx.Identifier{field.Name}.Sanitize())
	}
	return strings.Join(columns, ",")
}
