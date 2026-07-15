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

func TestCustomerChildCompanyMigration050BackfillsNullOwnershipIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := customerChildMigrationDatabaseThrough049(t, dsn)
	companyID, customerID, assetTypeID := seedCustomerChildMigrationParents(t, ctx, db, "success")

	var projectID, contactID, assetID, customerLocationID, otherLocationID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO projects(customer_id,name) VALUES($1,'Legacy project') RETURNING id`, customerID), &projectID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customer_contacts(customer_id,first_name) VALUES($1,'Legacy') RETURNING id`, customerID), &contactID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO assets(customer_id,asset_type_id,name) VALUES($1,$2,'Legacy asset') RETURNING id`, customerID, assetTypeID), &assetID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO locations(object_type,object_id,title) VALUES('customer',$1,'Legacy location') RETURNING id`, customerID), &customerLocationID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO locations(object_type,object_id,title) VALUES('company',$1,'Company location') RETURNING id`, companyID), &otherLocationID)

	if err := db.Migrate(ctx, customerChildMigrationFSThrough050(t)); err != nil {
		t.Fatalf("migrate 050: %v", err)
	}
	for table, id := range map[string]int64{"projects": projectID, "customer_contacts": contactID, "assets": assetID, "locations": customerLocationID} {
		var got *int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM `+table+` WHERE id=$1`, id), &got)
		if got == nil || *got != companyID {
			t.Errorf("%s company_id = %v, want %d", table, got, companyID)
		}
	}
	var otherCompanyID *int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM locations WHERE id=$1`, otherLocationID), &otherCompanyID)
	if otherCompanyID != nil {
		t.Fatalf("non-customer location company_id = %v, want NULL", *otherCompanyID)
	}
	var applied bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='050_customer_child_company_backfill')`), &applied)
	if !applied {
		t.Fatal("migration 050 was not recorded as applied")
	}

	down, err := fs.ReadFile(MigrationFS(), "050_customer_child_company_backfill.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("down 050: %v", err)
	}
	var afterDown *int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM projects WHERE id=$1`, projectID), &afterDown)
	if afterDown == nil || *afterDown != companyID {
		t.Fatalf("down changed backfilled ownership: %v", afterDown)
	}
}

func TestCustomerChildCompanyMigration050LeavesAmbiguousOwnershipForAuditIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := customerChildMigrationDatabaseThrough049(t, dsn)
	companyID, customerID, _ := seedCustomerChildMigrationParents(t, ctx, db, "owned")
	_, otherCustomerID, _ := seedCustomerChildMigrationParents(t, ctx, db, "mismatch")
	var legacyProjectID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO projects(customer_id,name) VALUES($1,'Legacy project') RETURNING id`, customerID), &legacyProjectID)
	var mismatchedContactID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customer_contacts(company_id,customer_id,first_name) VALUES($1,$2,'Mismatch') RETURNING id`, companyID, otherCustomerID), &mismatchedContactID)
	var unownedCustomerID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(display_name) VALUES('Unowned customer') RETURNING id`), &unownedCustomerID)
	var unownedContactID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customer_contacts(customer_id,first_name) VALUES($1,'Unowned parent') RETURNING id`, unownedCustomerID), &unownedContactID)
	var missingParentLocationID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO locations(object_type,object_id,title) VALUES('customer',9223372036854775807,'Missing parent') RETURNING id`), &missingParentLocationID)

	if err := db.Migrate(ctx, customerChildMigrationFSThrough050(t)); err != nil {
		t.Fatalf("migrate 050 with ambiguous legacy ownership: %v", err)
	}
	var projectCompanyID, mismatchedCompanyID, unownedCompanyID, missingParentCompanyID *int64
	var applied bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM projects WHERE id=$1`, legacyProjectID), &projectCompanyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM customer_contacts WHERE id=$1`, mismatchedContactID), &mismatchedCompanyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM customer_contacts WHERE id=$1`, unownedContactID), &unownedCompanyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM locations WHERE id=$1`, missingParentLocationID), &missingParentCompanyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='050_customer_child_company_backfill')`), &applied)
	if projectCompanyID == nil || *projectCompanyID != companyID {
		t.Errorf("owned project company_id = %v, want %d", projectCompanyID, companyID)
	}
	if mismatchedCompanyID == nil || *mismatchedCompanyID != companyID {
		t.Errorf("explicit mismatch company_id = %v, want unchanged %d", mismatchedCompanyID, companyID)
	}
	if unownedCompanyID != nil {
		t.Errorf("unowned-parent contact company_id = %v, want NULL", *unownedCompanyID)
	}
	if missingParentCompanyID != nil {
		t.Errorf("missing-parent location company_id = %v, want NULL", *missingParentCompanyID)
	}
	if !applied {
		t.Error("migration 050 was not recorded as applied")
	}
}

func seedCustomerChildMigrationParents(t *testing.T, ctx context.Context, db *DB, suffix string) (companyID, customerID, assetTypeID int64) {
	t.Helper()
	unique := fmt.Sprintf("%s-%d", suffix, time.Now().UnixNano())
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES($1,$2) RETURNING id`, "Company "+suffix, unique), &companyID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,$2) RETURNING id`, companyID, "Customer "+suffix), &customerID)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO asset_types(company_id,name) VALUES($1,$2) RETURNING id`, companyID, "Asset type "+suffix), &assetTypeID)
	return companyID, customerID, assetTypeID
}

func customerChildMigrationDatabaseThrough049(t *testing.T, dsn string) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	admin, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_customer_child_migration_%d", time.Now().UnixNano())
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

	through049 := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "050_" {
			continue
		}
		data, readErr := fs.ReadFile(MigrationFS(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		through049[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err = db.Migrate(ctx, through049); err != nil {
		t.Fatalf("migrate through 049: %v", err)
	}
	return db, ctx
}

func customerChildMigrationFSThrough050(t *testing.T) fs.FS {
	t.Helper()
	through050 := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "051_" {
			continue
		}
		data, readErr := fs.ReadFile(MigrationFS(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		through050[entry.Name()] = &fstest.MapFile{Data: data}
	}
	return through050
}
