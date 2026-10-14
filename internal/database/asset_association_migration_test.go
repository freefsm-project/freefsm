package database

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func reviewFixtureAssetAssociations(t *testing.T, ctx context.Context, db *DB) {
	t.Helper()
	review, err := db.AssetAssociationPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	review.Reviewer = "Fixture product Administrator"
	review.Reason = "Reviewed all observed relationships, unknown dates and cross-Customer links"
	review.AdministratorReviewed = true
	review.RetainObservedUnknownDates = true
	data, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "association-review.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FREEFSM_ASSET_ASSOCIATION_REVIEW_FILE", path)
}

func TestAssetAssociationUpgradeRequiresNonDestructiveReview(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn)
	var company, customer, typ, id int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Observed owner') RETURNING id`, company), &customer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT min(id) FROM asset_types`), &typ)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO assets(company_id,customer_id,asset_type_id,name) VALUES($1,$2,$3,'Observed asset') RETURNING id`, company, customer, typ), &id)
	var other, jobID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Earlier unknown owner') RETURNING id`, company), &other)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,asset_id,job_type) VALUES($1,$2,$3,'Retained cross-Customer evidence') RETURNING id`, company, other, id), &jobID)
	reviewFixtureAssetIdentities(t, ctx, db, AssetIdentityResolution{ID: id, Number: "OBSERVED-35", OwnershipClass: "customer_owned"})
	t.Setenv("FREEFSM_ASSET_ASSOCIATION_REVIEW_FILE", "")
	var before, after int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`), &before)
	if err := db.Migrate(ctx, MigrationFS()); err == nil {
		t.Fatal("association cutover accepted without explicit review")
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`), &after)
	if before != after {
		t.Fatal("missing review changed earlier migrations")
	}
	reviewFixtureAssetAssociations(t, ctx, db)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var invented int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM asset_association_events`), &invented)
	if invented != 0 {
		t.Fatal("migration invented association events")
	}
	var observed int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT customer_id FROM asset_association_periods WHERE asset_id=$1 AND starts_on IS NULL`, id), &observed)
	if observed != customer {
		t.Fatal("migration changed observed owner")
	}
	var retained bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT NOT operational AND linked_on IS NULL AND legacy AND review_reason LIKE 'Cross-Customer%' FROM job_asset_links WHERE job_id=$1 AND asset_id=$2`, jobID, id), &retained)
	if !retained {
		t.Fatal("cross-Customer evidence not preserved as unknown historical link")
	}
}
