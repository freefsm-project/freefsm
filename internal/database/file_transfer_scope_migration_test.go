package database

import (
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
)

func TestFileTransferScopeUpgradePreservesRecordedBytes(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	a := seedFileOwnershipTenant(t, ctx, db, "transfer-scope-history")
	insertMigrationFile(t, ctx, db, a.company, a.user, "customer", a.customer)
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "075_file_transfer_scope.up.sql" {
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
	const id = "85000000-0000-4000-8000-000000000001"
	if _, err := db.Pool.Exec(ctx, `INSERT INTO file_transfers(id,user_id,session_hash,publication_id,object_type,object_id,name,size,sha256,transferred) VALUES($1,$2,'recorded-client-key','85000000-0000-4000-8000-000000000002','customer',$3,'retained.txt',11,'b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9',11)`, id, a.user, a.customer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO file_transfer_chunks VALUES($1,0,$2)`, id, []byte("hello world")); err != nil {
		t.Fatal(err)
	}
	var beforeTransfer, beforeChunk, beforeFile, beforeGrants, afterTransfer, afterChunk, afterFile, afterGrants []byte
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(t) FROM file_transfers t`), &beforeTransfer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(c) FROM file_transfer_chunks c`), &beforeChunk)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(f) FROM files f`), &beforeFile)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g`), &beforeGrants)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(t) FROM file_transfers t`), &afterTransfer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(c)-'user_id'-'session_hash' FROM file_transfer_chunks c`), &afterChunk)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(f) FROM files f`), &afterFile)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g`), &afterGrants)
	if string(beforeTransfer) != string(afterTransfer) || string(beforeChunk) != string(afterChunk) || string(beforeFile) != string(afterFile) || string(beforeGrants) != string(afterGrants) {
		t.Fatal("upgrade rewrote recorded bytes, File identity, parent, attribution or grants")
	}
	var owner bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT c.user_id=t.user_id AND c.session_hash=t.session_hash FROM file_transfer_chunks c JOIN file_transfers t ON t.id=c.transfer_id`), &owner)
	if !owner {
		t.Fatal("chunk ownership differs from its recorded parent")
	}
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
}
