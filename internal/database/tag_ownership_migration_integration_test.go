package database

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestTagOwnershipMigration046BackfillsAndEnforcesOwnershipIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	t.Run("single company", func(t *testing.T) {
		db, ctx := tagMigrationDatabaseThrough045(t, dsn)
		var company, tag int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies`), &company)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(name) VALUES('legacy') RETURNING id`), &tag)
		if err := db.Migrate(ctx, MigrationFS()); err != nil {
			t.Fatal(err)
		}
		var owner int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM tags WHERE id=$1`, tag), &owner)
		if owner != company {
			t.Fatalf("owner=%d want %d", owner, company)
		}
		down, err := fs.ReadFile(MigrationFS(), "046_tag_ownership.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(ctx, string(down)); err != nil {
			t.Fatal(err)
		}
		var tagsNullable, linksNullable string
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='tags' AND column_name='company_id'`), &tagsNullable)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='tag_links' AND column_name='company_id'`), &linksNullable)
		if tagsNullable != "YES" || linksNullable != "NO" {
			t.Fatalf("down nullability tags=%s links=%s", tagsNullable, linksNullable)
		}
		var originalDeleteAction string
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT confdeltype::text FROM pg_constraint WHERE conrelid='tag_links'::regclass AND conname='tag_links_tag_id_fkey'`), &originalDeleteAction)
		if originalDeleteAction != "c" {
			t.Fatalf("down tag FK delete action=%q want cascade", originalDeleteAction)
		}
	})

	t.Run("multiple companies and trigger", func(t *testing.T) {
		db, ctx := tagMigrationDatabaseThrough045(t, dsn)
		var a, b, customerA, customerB, tag, estimate, invoice int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('A','tag-a') RETURNING id`), &a)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('B','tag-b') RETURNING id`), &b)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'A') RETURNING id`, a), &customerA)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'B') RETURNING id`, b), &customerB)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(name) VALUES('derived') RETURNING id`), &tag)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'customer',$3)`, a, tag, customerA); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, MigrationFS()); err != nil {
			t.Fatal(err)
		}
		var owner int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM tags WHERE id=$1`, tag), &owner)
		if owner != a {
			t.Fatalf("derived owner=%d want %d", owner, a)
		}
		if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'customer',$3)`, a, tag, customerB); err == nil {
			t.Fatal("trigger accepted cross-company target")
		}
		if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'customer',999999)`, a, tag); err == nil {
			t.Fatal("trigger accepted missing target")
		}
		if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'item',1)`, a, tag); err == nil {
			t.Fatal("trigger accepted unsupported target type")
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE customers SET company_id=$1 WHERE id=$2`, b, customerA); err == nil {
			t.Fatal("tagged target company transfer succeeded")
		}
		if _, err := db.Pool.Exec(ctx, `DELETE FROM customers WHERE id=$1`, customerA); err == nil {
			t.Fatal("tagged target hard delete succeeded")
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, customerA); err != nil {
			t.Fatalf("archive tagged target: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE tags SET company_id=$1 WHERE id=$2`, b, tag); err == nil {
			t.Fatal("linked tag company transfer succeeded")
		}

		var targetTriggers, forwardTriggers int
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE NOT t.tgisinternal AND n.nspname=current_schema() AND t.tgname IN ('customer_tag_ownership_guard','project_tag_ownership_guard','job_tag_ownership_guard','asset_tag_ownership_guard','estimate_tag_ownership_guard','invoice_tag_ownership_guard')`), &targetTriggers)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE NOT tgisinternal AND tgrelid='tag_links'::regclass AND tgname='tag_link_target_ownership'`), &forwardTriggers)
		if targetTriggers != 6 || forwardTriggers != 1 {
			t.Fatalf("target triggers=%d forward=%d", targetTriggers, forwardTriggers)
		}
		var deleteAction string
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT confdeltype::text FROM pg_constraint WHERE conrelid='tag_links'::regclass AND conname='tag_links_tag_company_fk'`), &deleteAction)
		if deleteAction != "r" {
			t.Fatalf("tag company FK delete action=%q want restrict", deleteAction)
		}
		var constraints int
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint c JOIN pg_class r ON r.oid=c.conrelid JOIN pg_namespace n ON n.oid=r.relnamespace WHERE n.nspname=current_schema() AND conname IN ('tags_company_fk','tag_links_company_fk','tags_id_company_unique','tag_links_tag_company_fk')`), &constraints)
		if constraints != 4 {
			t.Fatalf("046 constraints=%d want 4", constraints)
		}
		var strictColumns, functions, legacyTagFK int
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name IN ('tags','tag_links') AND column_name='company_id' AND is_nullable='NO'`), &strictColumns)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.proname IN ('validate_tag_link_target_ownership','guard_tagged_target_ownership')`), &functions)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid='tag_links'::regclass AND conname='tag_links_tag_id_fkey'`), &legacyTagFK)
		if strictColumns != 2 || functions != 2 || legacyTagFK != 0 {
			t.Fatalf("strict columns=%d functions=%d legacy FK=%d", strictColumns, functions, legacyTagFK)
		}

		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title,conversion_hidden_at) VALUES($1,$2,'hidden',now()) RETURNING id`, a, customerA), &estimate)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title,conversion_hidden_at) VALUES($1,$2,9001,'hidden',now()) RETURNING id`, a, customerA), &invoice)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'estimate',$3)`, a, tag, estimate); err != nil {
			t.Fatalf("hidden estimate target: %v", err)
		}
		if _, err := db.Pool.Exec(ctx, `UPDATE tag_links SET object_type='invoice',object_id=$1 WHERE tag_id=$2 AND object_type='estimate'`, invoice, tag); err != nil {
			t.Fatalf("conversion transfer: %v", err)
		}
	})
}

func TestTagOwnershipMigration046CleansSupportedOrphansIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	t.Run("single company removes orphan and down does not restore it", func(t *testing.T) {
		db, ctx := tagMigrationDatabaseThrough045(t, dsn)
		var company, tag int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies`), &company)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(name) VALUES('clone orphan') RETURNING id`), &tag)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'customer',999999)`, company, tag); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, MigrationFS()); err != nil {
			t.Fatal(err)
		}
		var owner, links int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM tags WHERE id=$1`, tag), &owner)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM tag_links WHERE tag_id=$1`, tag), &links)
		if owner != company || links != 0 {
			t.Fatalf("owner=%d links=%d", owner, links)
		}
		down, err := fs.ReadFile(MigrationFS(), "046_tag_ownership.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(ctx, string(down)); err != nil {
			t.Fatal(err)
		}
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM tag_links WHERE tag_id=$1`, tag), &links)
		if links != 0 {
			t.Fatalf("down restored %d deleted orphan links", links)
		}
	})

	t.Run("unsupported type fails without deletion", func(t *testing.T) {
		db, ctx := tagMigrationDatabaseThrough045(t, dsn)
		var company, tag int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies`), &company)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(company_id,name) VALUES($1,'unsupported') RETURNING id`, company), &tag)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'removed_type',999999)`, company, tag); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, MigrationFS()); err == nil {
			t.Fatal("migration accepted unsupported target type")
		}
		var links int
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM tag_links WHERE tag_id=$1`, tag), &links)
		if links != 1 {
			t.Fatalf("unsupported link count=%d want 1", links)
		}
	})

	t.Run("existing cross-company target fails without deletion", func(t *testing.T) {
		db, ctx := tagMigrationDatabaseThrough045(t, dsn)
		a, b, _, customerB := seedTagCompanies(t, ctx, db)
		var tag int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(company_id,name) VALUES($1,'cross target') RETURNING id`, a), &tag)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'customer',$3)`, a, tag, customerB); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, MigrationFS()); err == nil {
			t.Fatal("migration accepted cross-company target")
		}
		var company, links int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT company_id FROM tag_links WHERE tag_id=$1`, tag), &company)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM tag_links WHERE tag_id=$1`, tag), &links)
		if company != a || company == b || links != 1 {
			t.Fatalf("company=%d links=%d", company, links)
		}
	})

	t.Run("multi-company null tag with only orphan evidence fails and rolls back", func(t *testing.T) {
		db, ctx := tagMigrationDatabaseThrough045(t, dsn)
		a, _, _, _ := seedTagCompanies(t, ctx, db)
		var tag int64
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(name) VALUES('ambiguous orphan') RETURNING id`), &tag)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'customer',999999)`, a, tag); err != nil {
			t.Fatal(err)
		}
		if err := db.Migrate(ctx, MigrationFS()); err == nil {
			t.Fatal("migration derived ownership from deleted orphan")
		}
		var links int
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM tag_links WHERE tag_id=$1`, tag), &links)
		if links != 1 {
			t.Fatalf("failed migration did not roll back orphan cleanup: links=%d", links)
		}
	})
}

func TestTagOwnershipMigration046WaitsForTargetWritersIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn)
	var company, customer int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'locked') RETURNING id`, company), &customer)
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE customers SET display_name='writer' WHERE id=$1`, customer); err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if err = db.Migrate(blockedCtx, MigrationFS()); err == nil {
		t.Fatal("migration did not wait for target writer")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatalf("migration after writer rollback: %v", err)
	}
}

func TestTagOwnershipMigration046DoesNotDeadlockConversionIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn)
	var company, customer, tag, estimate, invoice int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'conversion race') RETURNING id`, company), &customer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(company_id,name) VALUES($1,'conversion race') RETURNING id`, company), &tag)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title) VALUES($1,$2,'source') RETURNING id`, company, customer), &estimate)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title) VALUES($1,$2,9100,'target') RETURNING id`, company, customer), &invoice)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'estimate',$3)`, company, tag, estimate); err != nil {
		t.Fatal(err)
	}

	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE estimates SET title=title WHERE id=$1`, estimate); err != nil {
		t.Fatal(err)
	}
	migrationResult := make(chan error, 1)
	go func() { migrationResult <- db.Migrate(ctx, MigrationFS()) }()
	select {
	case err = <-migrationResult:
		t.Fatalf("migration did not wait for conversion target lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err = tx.Exec(ctx, `UPDATE tag_links SET object_type='invoice',object_id=$1 WHERE company_id=$2 AND tag_id=$3`, invoice, company, tag); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("conversion relation transfer deadlocked or failed: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-migrationResult:
		if err != nil {
			t.Fatalf("migration after conversion: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("migration and conversion deadlocked")
	}
}

func TestTagOwnershipConstraintsCloseConcurrentDeleteRacesIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := tagMigrationDatabaseThrough045(t, dsn)
	var company, customer, tag int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`), &company)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'race') RETURNING id`, company), &customer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(company_id,name) VALUES($1,'race') RETURNING id`, company), &tag)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, deleteSQL string
		id              int64
	}{
		{"tag", `DELETE FROM tags WHERE id=$1`, tag},
		{"target", `DELETE FROM customers WHERE id=$1`, customer},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Pool.Exec(ctx, `DELETE FROM tag_links WHERE tag_id=$1`, tag); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Pool.BeginTx(ctx, pgx.TxOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'customer',$3)`, company, tag, customer); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() { _, deleteErr := db.Pool.Exec(ctx, tc.deleteSQL, tc.id); result <- deleteErr }()
			select {
			case err := <-result:
				t.Fatalf("delete completed before attach commit: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-result; err == nil {
				t.Fatal("concurrent delete succeeded and silently lost link")
			}
		})
	}
}

func TestTagOwnershipMigration046PreflightRollsBackIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	cases := []struct {
		name string
		seed func(*testing.T, context.Context, *DB)
	}{
		{"unlinked null tag", func(t *testing.T, ctx context.Context, db *DB) {
			seedTagCompanies(t, ctx, db)
			_, err := db.Pool.Exec(ctx, `INSERT INTO tags(name) VALUES('orphan')`)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"links span companies", func(t *testing.T, ctx context.Context, db *DB) {
			a, b, ca, cb := seedTagCompanies(t, ctx, db)
			var tag int64
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(name) VALUES('split') RETURNING id`), &tag)
			_, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$3,'customer',$4),($2,$3,'customer',$5)`, a, b, tag, ca, cb)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"null link ownership", func(t *testing.T, ctx context.Context, db *DB) {
			a, _, ca, _ := seedTagCompanies(t, ctx, db)
			var tag int64
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(company_id,name) VALUES($1,'null link') RETURNING id`, a), &tag)
			if _, err := db.Pool.Exec(ctx, `ALTER TABLE tag_links ALTER COLUMN company_id DROP NOT NULL`); err != nil {
				t.Fatal(err)
			}
			_, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(tag_id,object_type,object_id) VALUES($1,'customer',$2)`, tag, ca)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{"tag link mismatch", func(t *testing.T, ctx context.Context, db *DB) {
			a, b, _, cb := seedTagCompanies(t, ctx, db)
			var tag int64
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO tags(company_id,name) VALUES($1,'mismatch') RETURNING id`, a), &tag)
			_, err := db.Pool.Exec(ctx, `INSERT INTO tag_links(company_id,tag_id,object_type,object_id) VALUES($1,$2,'customer',$3)`, b, tag, cb)
			if err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, ctx := tagMigrationDatabaseThrough045(t, dsn)
			tc.seed(t, ctx, db)
			if err := db.Migrate(ctx, MigrationFS()); err == nil {
				t.Fatal("migration succeeded")
			}
			var applied bool
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='046_tag_ownership')`), &applied)
			if applied {
				t.Fatal("failed migration was recorded")
			}
		})
	}
}

func seedTagCompanies(t *testing.T, ctx context.Context, db *DB) (a, b, ca, cb int64) {
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('A',$1) RETURNING id`, fmt.Sprintf("a-%d", time.Now().UnixNano())), &a)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('B',$1) RETURNING id`, fmt.Sprintf("b-%d", time.Now().UnixNano())), &b)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'A') RETURNING id`, a), &ca)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'B') RETURNING id`, b), &cb)
	return
}

func tagMigrationDatabaseThrough045(t *testing.T, dsn string) (*DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	admin, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("freefsm_tag_migration_%d", time.Now().UnixNano())
	if _, err = admin.Pool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Pool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	db, err := Connect(ctx, migrationSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	through045 := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "046_" {
			continue
		}
		data, e := fs.ReadFile(MigrationFS(), entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		through045[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err = db.Migrate(ctx, through045); err != nil {
		t.Fatal(err)
	}
	return db, ctx
}
