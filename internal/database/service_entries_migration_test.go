package database

import (
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	"golang.org/x/crypto/bcrypt"
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

func TestServiceEntryBatchReceiptUpgradePreservesRecordedHistory(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn, "125_")
	var company, user, customer, typ, asset, job, entry int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
	hash, err := bcrypt.GenerateFromPassword([]byte("upgrade-fixture-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,name,email,password_hash,role,authorization_role_id) VALUES($1,'Recorded actor','service-upgrade@test',$2,'admin',1) RETURNING id`, company, string(hash)), &user)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Recorded Customer') RETURNING id`, company), &customer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT min(id) FROM asset_types`), &typ)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO assets(company_id,customer_id,asset_type_id,name,ownership_class) VALUES($1,$2,$3,'Recorded pump','customer_owned') RETURNING id`, company, customer, typ), &asset)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,asset_id,job_type) VALUES($1,$2,$3,'Recorded service') RETURNING id`, company, customer, asset), &job)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO asset_service_entries(company_id,asset_id,job_id,link_id,customer_id,association_event_id,actor_id) SELECT $1,$2,$3,id,$4,association_event_id,$5 FROM job_asset_links WHERE job_id=$3 RETURNING id`, company, asset, job, customer, user), &entry)
	const revision = "87000000-0000-4000-8000-000000000001"
	if _, err := db.Pool.Exec(ctx, `INSERT INTO asset_service_revisions(id,entry_id,service_date,summary,actor_id) VALUES($1,$2,current_date,'Recorded original summary',$3);`, revision, entry, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO asset_service_responsible_users(revision_id,user_id) VALUES($1,$2)`, revision, user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO service_entry_operation_receipts(operation_id,user_id,client_key,intent,entry_id,code) VALUES('87000000-0000-4000-8000-000000000002',$1,'installed-client','{"recorded":"intent"}',$2,'accepted')`, user, entry); err != nil {
		t.Fatal(err)
	}
	const snapshot = `SELECT jsonb_build_object('entries',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM asset_service_entries e),'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM asset_service_revisions r),'responsibles',(SELECT jsonb_agg(to_jsonb(u)) FROM asset_service_responsible_users u),'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY operation_id) FROM service_entry_operation_receipts r),'blockers',(SELECT jsonb_agg(to_jsonb(b)) FROM job_customer_blockers b),'links',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM job_asset_links l),'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g))::text`
	var before, after string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, snapshot), &before)
	for range 2 {
		if err := db.Migrate(ctx, MigrationFS()); err != nil {
			t.Fatal(err)
		}
		mustMigrationScan(t, db.Pool.QueryRow(ctx, snapshot), &after)
		if before != after {
			t.Fatal("batch receipt upgrade changed installed history, identity or grants")
		}
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE service_entry_operation_receipts SET code='failed_dependency'`); err == nil {
		t.Fatal("upgrade lost receipt immutability")
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO service_entry_operation_receipts(operation_id,user_id,client_key,intent,code) VALUES('87000000-0000-4000-8000-000000000003',$1,'installed-client','{}','failed_dependency')`, user); err != nil {
		t.Fatal(err)
	}
}
