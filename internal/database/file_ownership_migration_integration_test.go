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

func TestFileOwnershipMigration047ValidDataAndEnforcementIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	a := seedFileOwnershipTenant(t, ctx, db, "a")
	b := seedFileOwnershipTenant(t, ctx, db, "b")
	targets := seedFileOwnershipTargets(t, ctx, db, a)

	for objectType, objectID := range targets {
		insertMigrationFile(t, ctx, db, a.company, a.user, objectType, objectID)
	}
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}

	var files, constraints, targetTriggers, forwardTriggers int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE company_id=$1`, a.company), &files)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='files'::regclass AND conname IN ('files_company_fk','files_uploader_company_fk')`), &constraints)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE NOT t.tgisinternal AND n.nspname=current_schema() AND t.tgname IN ('customer_file_ownership_guard','project_file_ownership_guard','job_file_ownership_guard','asset_file_ownership_guard','estimate_file_ownership_guard','invoice_file_ownership_guard')`), &targetTriggers)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal AND tgrelid='files'::regclass AND tgname='file_target_ownership'`), &forwardTriggers)
	if files != 6 || constraints != 2 || targetTriggers != 6 || forwardTriggers != 1 {
		t.Fatalf("files=%d constraints=%d target triggers=%d forward triggers=%d", files, constraints, targetTriggers, forwardTriggers)
	}

	invalidWrites := []struct {
		name string
		sql  string
		args []any
	}{
		{"cross-company target", migrationFileInsertSQL, []any{a.company, "customer", b.customer, a.user}},
		{"cross-company uploader", migrationFileInsertSQL, []any{a.company, "customer", a.customer, b.user}},
		{"missing target", migrationFileInsertSQL, []any{a.company, "customer", int64(999999), a.user}},
		{"unsupported target", migrationFileInsertSQL, []any{a.company, "item", int64(1), a.user}},
		{"missing company", migrationFileInsertSQL, []any{int64(999999), "customer", a.customer, a.user}},
	}
	for _, tc := range invalidWrites {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Pool.Exec(ctx, tc.sql, tc.args...); err == nil {
				t.Fatal("invalid file write succeeded")
			}
		})
	}

	guardedCustomer := seedFileOwnershipTenant(t, ctx, db, "guarded")
	insertMigrationFile(t, ctx, db, guardedCustomer.company, guardedCustomer.user, "customer", guardedCustomer.customer)
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET company_id=$1 WHERE id=$2`, b.company, guardedCustomer.customer); err == nil {
		t.Fatal("referenced target company transfer succeeded")
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM customers WHERE id=$1`, guardedCustomer.customer); err == nil {
		t.Fatal("referenced target delete succeeded")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, guardedCustomer.customer); err != nil {
		t.Fatalf("archive referenced target: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE customers SET company_id=company_id WHERE id=$1`, guardedCustomer.customer); err != nil {
		t.Fatalf("same-company target update: %v", err)
	}

	var conversionFile int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, migrationFileInsertSQL+` RETURNING id`, a.company, "estimate", targets["estimate"], a.user), &conversionFile)
	if _, err := db.Pool.Exec(ctx, `UPDATE files SET object_type='invoice',object_id=$1 WHERE id=$2`, targets["invoice"], conversionFile); err != nil {
		t.Fatalf("same-company conversion target transfer: %v", err)
	}
}

func TestFileOwnershipMigration047PreflightRollsBackIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	cases := []struct {
		name string
		seed func(*testing.T, context.Context, *DB, fileOwnershipTenant, fileOwnershipTenant)
	}{
		{"target company mismatch", func(t *testing.T, ctx context.Context, db *DB, a, b fileOwnershipTenant) {
			insertMigrationFile(t, ctx, db, a.company, a.user, "customer", b.customer)
		}},
		{"uploader company mismatch", func(t *testing.T, ctx context.Context, db *DB, a, b fileOwnershipTenant) {
			insertMigrationFile(t, ctx, db, a.company, b.user, "customer", a.customer)
		}},
		{"unsupported target", func(t *testing.T, ctx context.Context, db *DB, a, _ fileOwnershipTenant) {
			insertMigrationFile(t, ctx, db, a.company, a.user, "removed_type", 999999)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, ctx := fileMigrationDatabaseThrough046(t, dsn)
			a := seedFileOwnershipTenant(t, ctx, db, "preflight-a")
			b := seedFileOwnershipTenant(t, ctx, db, "preflight-b")
			tc.seed(t, ctx, db, a, b)
			if err := db.Migrate(ctx, MigrationFS()); err == nil {
				t.Fatal("migration accepted invalid file ownership")
			}
			var rows, constraints int
			var applied bool
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM files`), &rows)
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='047_file_ownership')`), &applied)
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='files'::regclass AND conname IN ('files_company_fk','files_uploader_company_fk')`), &constraints)
			if rows != 1 || applied || constraints != 0 {
				t.Fatalf("rows=%d applied=%v constraints=%d", rows, applied, constraints)
			}
		})
	}
}

func TestFileOwnershipMigration047DownRemovesOnlyItsEnforcementIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	down, err := fs.ReadFile(MigrationFS(), "047_file_ownership.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}

	var constraints, fileTriggers, tagTriggers int
	var nullable string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='files'::regclass AND conname IN ('files_company_fk','files_uploader_company_fk')`), &constraints)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE NOT t.tgisinternal AND n.nspname=current_schema() AND (t.tgname='file_target_ownership' OR t.tgname LIKE '%_file_ownership_guard')`), &fileTriggers)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE NOT t.tgisinternal AND n.nspname=current_schema() AND t.tgname='tag_link_target_ownership'`), &tagTriggers)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='files' AND column_name='company_id'`), &nullable)
	if constraints != 0 || fileTriggers != 0 || tagTriggers != 1 || nullable != "NO" {
		t.Fatalf("constraints=%d file triggers=%d tag triggers=%d file company nullable=%s", constraints, fileTriggers, tagTriggers, nullable)
	}
}

type fileOwnershipTenant struct {
	company  int64
	user     int64
	customer int64
}

func seedFileOwnershipTenant(t *testing.T, ctx context.Context, db *DB, suffix string) fileOwnershipTenant {
	t.Helper()
	var tenant fileOwnershipTenant
	unique := fmt.Sprintf("%s-%d", suffix, time.Now().UnixNano())
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES($1,$2) RETURNING id`, "Files "+suffix, "files-"+unique), &tenant.company)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,$2,'x','Uploader','admin') RETURNING id`, tenant.company, unique+"@example.test"), &tenant.user)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,$2) RETURNING id`, tenant.company, "Customer "+suffix), &tenant.customer)
	return tenant
}

func seedFileOwnershipTargets(t *testing.T, ctx context.Context, db *DB, tenant fileOwnershipTenant) map[string]int64 {
	t.Helper()
	targets := map[string]int64{"customer": tenant.customer}
	var project, job, assetType, asset, estimate, invoice int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO projects(company_id,customer_id,name) VALUES($1,$2,'Project') RETURNING id`, tenant.company, tenant.customer), &project)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Job') RETURNING id`, tenant.company, tenant.customer), &job)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO asset_types(company_id,name) VALUES($1,'Asset type') RETURNING id`, tenant.company), &assetType)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO assets(company_id,customer_id,asset_type_id,name) VALUES($1,$2,$3,'Asset') RETURNING id`, tenant.company, tenant.customer, assetType), &asset)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title) VALUES($1,$2,'Estimate') RETURNING id`, tenant.company, tenant.customer), &estimate)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title) VALUES($1,$2,47001,'Invoice') RETURNING id`, tenant.company, tenant.customer), &invoice)
	targets["project"] = project
	targets["job"] = job
	targets["asset"] = asset
	targets["estimate"] = estimate
	targets["invoice"] = invoice
	return targets
}

const migrationFileInsertSQL = `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by) VALUES($1,$2,$3,'original','stored','text/plain',1,'path',$4)`

func insertMigrationFile(t *testing.T, ctx context.Context, db *DB, company, uploader int64, objectType string, objectID int64) {
	t.Helper()
	if _, err := db.Pool.Exec(ctx, migrationFileInsertSQL, company, objectType, objectID, uploader); err != nil {
		t.Fatal(err)
	}
}

func fileMigrationDatabaseThrough046(t *testing.T, dsn string) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	admin, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_file_migration_%d", time.Now().UnixNano())
	if _, err = admin.Pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Pool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	db, err := Connect(ctx, migrationSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	through046 := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "047_" {
			continue
		}
		data, readErr := fs.ReadFile(MigrationFS(), entry.Name())
		if readErr != nil {
			t.Fatal(readErr)
		}
		through046[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err = db.Migrate(ctx, through046); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}
