package database

import (
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
)

func TestFilePublicationUpgradePreservesRecordedFacts(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	a := seedFileOwnershipTenant(t, ctx, db, "publication-history")
	insertMigrationFile(t, ctx, db, a.company, a.user, "customer", a.customer)
	var before, after []byte
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(f) FROM files f`), &before)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(f)-'revision'-'removed_at'-'removed_by'-'sha256' FROM files f`), &after)
	if string(before) != string(after) {
		t.Fatalf("migration rewrote history: %s -> %s", before, after)
	}
	var honest bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT revision=1 AND removed_at IS NULL AND removed_by IS NULL AND sha256 IS NULL FROM files`), &honest)
	if !honest {
		t.Fatal("migration invented validation or removal history")
	}
}

func TestFileAPIAttributionUpgradeDoesNotInventHistory(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	a := seedFileOwnershipTenant(t, ctx, db, "attribution-history")
	insertMigrationFile(t, ctx, db, a.company, a.user, "customer", a.customer)
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "070_" {
			continue
		}
		data, err := fs.ReadFile(MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err := db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO file_transfers(id,user_id,session_hash,publication_id,object_type,object_id,name,size,sha256) VALUES('46000000-0000-4000-8000-000000000001',$1,'recorded-client-key','46000000-0000-4000-8000-000000000002','customer',$2,'legacy.txt',1,repeat('0',64))`, a.user, a.customer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO authorization_audit(actor_id,role_id,action,scope,target_type,target_id,channel,detail) SELECT $1,1,'file.upload','A','file',id,'api','{"publication_id":"46000000-0000-4000-8000-000000000002"}' FROM files`, a.user); err != nil {
		t.Fatal(err)
	}
	var beforeTransfer, beforeAudit, afterTransfer, afterAudit []byte
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(t) FROM file_transfers t`), &beforeTransfer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(a) FROM authorization_audit a WHERE target_type='file'`), &beforeAudit)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(t)-'occurred_at' FROM file_transfers t`), &afterTransfer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(a) FROM authorization_audit a WHERE target_type='file'`), &afterAudit)
	var honest bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT occurred_at IS NULL AND NOT EXISTS(SELECT 1 FROM file_mutation_operations) FROM file_transfers`), &honest)
	if !honest || string(beforeTransfer) != string(afterTransfer) || string(beforeAudit) != string(afterAudit) {
		t.Fatal("upgrade fabricated or rewrote historical API attribution")
	}
}
