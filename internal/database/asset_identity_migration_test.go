package database

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewFixtureAssetIdentities(t *testing.T, ctx context.Context, db *DB, resolutions ...AssetIdentityResolution) {
	t.Helper()
	review, err := db.AssetIdentityPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	review.Reviewer = "Fixture reviewer"
	review.Reason = "Explicitly reviewed fixture equipment ownership and Number"
	review.Resolutions = resolutions
	data, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "asset-review.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FREEFSM_ASSET_REVIEW_FILE", path)
	reviewFixtureAssetAssociations(t, ctx, db)
}

func TestAssetIdentityReviewedUpgradePreservesReferences(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn)
	var company, customer, typ, asset, job int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Legacy owner') RETURNING id`, company), &customer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM asset_types ORDER BY id LIMIT 1`), &typ)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO assets(company_id,customer_id,asset_type_id,name,serial_number,notes,deleted_at) VALUES($1,$2,$3,'Legacy pump','duplicate serial','Recorded instructions',now()) RETURNING id`, company, customer, typ), &asset)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,asset_id,job_type,deleted_at) VALUES($1,$2,$3,'Recorded service',now()) RETURNING id`, company, customer, asset), &job)
	var before, after int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`), &before)
	inventory, err := db.AssetIdentityPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inventory.Evidence), `"association_start": null`) || !strings.Contains(string(inventory.Evidence), "Recorded service") && !strings.Contains(string(inventory.Evidence), "job_references") {
		t.Fatalf("missing honest association evidence: %s", inventory.Evidence)
	}
	if err = db.Migrate(ctx, MigrationFS()); err == nil {
		t.Fatal("unknown ownership and Number cut over without review")
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`), &after)
	if before != after {
		t.Fatal("unreviewed cutover applied earlier migrations")
	}
	reviewFixtureAssetIdentities(t, ctx, db, AssetIdentityResolution{ID: asset, Number: "LEGACY-PUMP", OwnershipClass: "company_owned"})
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	// New migration runner instance models a later startup; facts are persisted.
	if err = (&DB{Pool: db.Pool}).Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var number, class, notes string
	var owner, linked int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT number,ownership_class,notes,customer_id,(SELECT asset_id FROM jobs WHERE id=$2) FROM assets WHERE id=$1 AND deleted_at IS NOT NULL`, asset, job), &number, &class, &notes, &owner, &linked)
	if number != "LEGACY-PUMP" || class != "company_owned" || notes != "Recorded instructions" || owner != customer || linked != asset {
		t.Fatal("upgrade changed recorded facts")
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE asset_identity_migration_evidence SET reason='rewrite'`); err == nil {
		t.Fatal("review evidence is mutable")
	}
	if _, err = db.Pool.Exec(ctx, `DELETE FROM assets WHERE id=$1`, asset); err == nil {
		t.Fatal("retained identity deleted")
	}
}

func TestAssetIdentityReviewRejectsStaleDuplicateAndChangedKnownMappings(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn)
	if _, err := db.Pool.Exec(ctx, `ALTER TABLE assets ADD number text; ALTER TABLE assets ADD ownership_class text;
 INSERT INTO customers(id,display_name) VALUES(34001,'Known owner');
 INSERT INTO assets(id,customer_id,asset_type_id,name,number,ownership_class) VALUES(34001,34001,(SELECT min(id) FROM asset_types),'Known','KNOWN-34','customer_owned'),(34002,34001,(SELECT min(id) FROM asset_types),'Unknown',NULL,NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, resolutions := range [][]AssetIdentityResolution{
		{{ID: 34001, Number: "CHANGED", OwnershipClass: "customer_owned"}, {ID: 34002, Number: "NEW", OwnershipClass: "company_owned"}},
		{{ID: 34001, Number: "KNOWN-34", OwnershipClass: "company_owned"}, {ID: 34002, Number: "NEW", OwnershipClass: "company_owned"}},
		{{ID: 34001, Number: "KNOWN-34", OwnershipClass: "customer_owned"}, {ID: 34002, Number: "KNOWN-34", OwnershipClass: "company_owned"}},
	} {
		reviewFixtureAssetIdentities(t, ctx, db, resolutions...)
		if err := db.Migrate(ctx, MigrationFS()); err == nil {
			t.Fatal("invalid reviewed mapping accepted")
		}
	}
	reviewFixtureAssetIdentities(t, ctx, db, AssetIdentityResolution{ID: 34001, Number: "KNOWN-34", OwnershipClass: "customer_owned"}, AssetIdentityResolution{ID: 34002, Number: "NEW", OwnershipClass: "company_owned"})
	if _, err := db.Pool.Exec(ctx, `UPDATE assets SET name='Changed after review' WHERE id=34002`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, MigrationFS()); err == nil {
		t.Fatal("stale identity review accepted")
	}
}
