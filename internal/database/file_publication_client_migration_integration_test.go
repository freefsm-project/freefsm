package database

import (
	"fmt"
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
)

func TestFilePublicationClientUpgradeUsesOnlyRecordedOrigin(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	a := seedFileOwnershipTenant(t, ctx, db, "publication-client-history")
	insertMigrationFile(t, ctx, db, a.company, a.user, "customer", a.customer)
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "073_" {
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
	// API attribution survives deletion/expiry of session rows: only immutable
	// upload audit, not a currently existing session/transfer, establishes origin.
	for i := 1; i <= 4; i++ {
		op := fmt.Sprintf("46000000-0000-4000-8000-%012d", i)
		if _, err := db.Pool.Exec(ctx, `INSERT INTO file_publications(user_id,operation_id,intent,file_id) SELECT $1,$2,'recorded intent',id FROM files`, a.user, op); err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			continue
		} // unknown origin
		channel, detail := "api", fmt.Sprintf(`{"operation_id":%q,"session_id":746}`, op)
		if i == 2 {
			channel = "web"
			detail = fmt.Sprintf(`{"publication_id":%q}`, op)
		}
		if _, err := db.Pool.Exec(ctx, `INSERT INTO authorization_audit(actor_id,role_id,action,scope,target_type,target_id,channel,detail) SELECT $1,1,'file.upload','A','file',id,$2,$3::jsonb FROM files`, a.user, channel, detail); err != nil {
			t.Fatal(err)
		}
		if i == 4 { // conflicting historical evidence must not pick either device
			if _, err := db.Pool.Exec(ctx, `INSERT INTO authorization_audit(actor_id,role_id,action,scope,target_type,target_id,channel,detail) SELECT $1,1,'file.upload','A','file',id,'api',$2::jsonb FROM files`, a.user, fmt.Sprintf(`{"operation_id":%q,"session_id":747}`, op)); err != nil {
				t.Fatal(err)
			}
		}
	}
	var before, after, auditBefore, auditAfter []byte
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(p) ORDER BY operation_id) FROM file_publications p`), &before)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM authorization_audit a`), &auditBefore)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(p)-'client_key' ORDER BY operation_id) FROM file_publications p`), &after)
	// 074 adds nullable evidence columns, never historical identity backfills.
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(a)-ARRAY['actor_identity','actor_name','role_identity','role_name','device_name','operation_id','occurred_at','session_id'] ORDER BY id) FROM authorization_audit a`), &auditAfter)
	var unknown bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT bool_and(actor_identity IS NULL AND actor_name IS NULL AND role_identity IS NULL AND role_name IS NULL AND device_name IS NULL AND operation_id IS NULL AND occurred_at IS NULL AND session_id IS NULL) FROM authorization_audit`), &unknown)
	if !unknown {
		t.Fatal("upgrade invented event-time identity or API evidence")
	}
	if string(before) != string(after) || string(auditBefore) != string(auditAfter) {
		t.Fatal("upgrade rewrote recorded publication/audit facts")
	}
	for i, want := range []string{"api:746", "web", "", ""} {
		var got string
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT coalesce(client_key,'') FROM file_publications WHERE operation_id=$1`, fmt.Sprintf("46000000-0000-4000-8000-%012d", i+1)), &got)
		if got != want {
			t.Fatalf("origin %d: got %q want %q", i+1, got, want)
		}
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE file_publications SET client_key='api:999'`); err == nil {
		t.Fatal("publication origin can be reassigned")
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM file_publications`); err == nil {
		t.Fatal("publication evidence can be discarded")
	}
}
