package database

import (
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
)

func TestServiceEntryUpgradePreservesUnknownEvidenceWithoutFabrication(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn)
	var company, customer, typ, asset, job int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Observed Customer') RETURNING id`, company), &customer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT min(id) FROM asset_types`), &typ)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO assets(company_id,customer_id,asset_type_id,name) VALUES($1,$2,$3,'Legacy equipment') RETURNING id`, company, customer, typ), &asset)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,asset_id,job_type,notes) VALUES($1,$2,$3,'Legacy linked Job','Unknown service date and responsible users: retain original narrative') RETURNING id`, company, customer, asset), &job)
	reviewFixtureAssetIdentities(t, ctx, db, AssetIdentityResolution{ID: asset, Number: "SERVICE-LEGACY", OwnershipClass: "customer_owned"})
	reviewFixtureAssetAssociations(t, ctx, db)
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "085_" {
			continue
		}
		raw, err := fs.ReadFile(MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	if err = db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	const snapshot = `SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM jobs j),'assets',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM assets a),'links',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM job_asset_links l),'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g))::text`
	var before, after string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, snapshot), &before)
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, snapshot), &after)
	if before != after {
		t.Fatal("Service Entry upgrade changed original evidence or grants")
	}
	var count int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_entries`), &count)
	if count != 0 {
		t.Fatal("legacy Job text fabricated Service Entries")
	}
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
}
