package database

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestJobPricingUpgradePreservesSnapshotsAndStopsUnknownLegacy(t *testing.T) {
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
		// Isolate this preserving cutover from other composed migrations that
		// legitimately add columns/revisions to the surrounding Job schema.
		if e.Name() != "076_job_pricing.up.sql" {
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
	_, err = db.Pool.Exec(ctx, `INSERT INTO customers(id,company_id,display_name) SELECT 4401,id,'Historical customer' FROM companies LIMIT 1;
INSERT INTO items(id,name,type,unit_price,unit_cost) VALUES(4401,'Recorded source','service',90,33);
INSERT INTO jobs(id,company_id,customer_id,job_type,line_items) SELECT 4401,id,4401,'Recorded work','[{"item_id":4401,"title":"Recorded capture","description":"Historical description","quantity":2,"unit_price":17.25,"taxable":true,"tax_rate":"6.5"},{"title":"Historical free text","quantity":1,"unit_price":9}]' FROM companies LIMIT 1;
INSERT INTO jobs(id,company_id,customer_id,job_type,line_items) SELECT 4402,id,4401,'Needs Administrator review','[{"title":"Ambiguous legacy field","quantity":1,"unit_price":9,"unit_cost":7}]' FROM companies LIMIT 1;
INSERT INTO authorization_roles(name) VALUES('Reviewed existing empty Role');`)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := `SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j),'items',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM items i),'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g))::text`
	var before, after string
	if err := db.Pool.QueryRow(ctx, snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, MigrationFS()); err == nil || !strings.Contains(err.Error(), "4402") {
		t.Fatalf("unknown legacy field must stop cutover for review: %v", err)
	}
	if err := db.Pool.QueryRow(ctx, snapshot).Scan(&after); err != nil || before != after {
		t.Fatalf("rejected upgrade changed recorded facts: %v", err)
	}
	// This disposable fixture explicitly represents the Administrator's reviewed
	// resolution, not an automatic migration mapping or inference of provenance.
	if _, err := db.Pool.Exec(ctx, `UPDATE jobs SET line_items='[{"title":"Reviewed free text","quantity":1,"unit_price":9}]' WHERE id=4402`); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, snapshot).Scan(&after); err != nil || before != after {
		t.Fatalf("upgrade changed snapshots, provenance or grants: %v", err)
	}
}
