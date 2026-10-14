package database

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"golang.org/x/crypto/bcrypt"
)

func TestOperationAdmissionInstalledReceiptsPreserved(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), "082_") {
			continue
		}
		b, err := fs.ReadFile(MigrationFS(), e.Name())
		if err != nil {
			t.Fatal(err)
		}
		prior[e.Name()] = &fstest.MapFile{Data: b}
	}
	if err := db.Migrate(ctx, prior); err != nil {
		t.Fatal(err)
	}
	hash,err:=bcrypt.GenerateFromPassword([]byte("Upgrade-test-Password1!"),bcrypt.MinCost);if err!=nil{t.Fatal(err)}
	if _,err:=db.Pool.Exec(ctx,`INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 8201,id,'admission-upgrade@test','Recorded actor',$1,'admin',1 FROM companies LIMIT 1`,string(hash));err!=nil{t.Fatal(err)}
	if _, err := db.Pool.Exec(ctx, `
	INSERT INTO job_execution_receipts(user_id,operation_id,client_key,intent_hash,code) VALUES
	(8201,'82000000-0000-4000-8000-000000000001','api:recorded','original-intent','invalid_intent'),
	(8201,'82000000-0000-4000-8000-000000000002','api:recorded','original-intent','accepted');
	INSERT INTO time_clock_receipts(user_id,operation_id,client_key,intent_hash,code,accepted_at) VALUES
	(8201,'82000000-0000-4000-8000-000000000002','api:recorded','different-historical-intent','accepted','2020-01-02T03:04:05Z');`); err != nil {
		t.Fatal(err)
	}
	const snapshot = `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM job_execution_receipts x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY operation_id) FROM time_clock_receipts x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY role_id,permission) FROM authorization_grants x))::text`
	var before, after string
	if err := db.Pool.QueryRow(ctx, snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, snapshot).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("migration rewrote original receipts or grants")
	}
	var kind, client string
	var original bool
	if err := db.Pool.QueryRow(ctx, `SELECT operation_type,client_key,intent IS NULL FROM operation_admissions WHERE operation_id='82000000-0000-4000-8000-000000000001'`).Scan(&kind, &client, &original); err != nil {
		t.Fatal(err)
	}
	if kind != "job.execution" || client != "api:recorded" || !original {
		t.Fatalf("lost source reservation: %s %s %v", kind, client, original)
	}
	if err := db.Pool.QueryRow(ctx, `SELECT operation_type FROM operation_admissions WHERE operation_id='82000000-0000-4000-8000-000000000002'`).Scan(&kind); err != nil {
		t.Fatal(err)
	}
	if kind != "legacy.identity_conflict" {
		t.Fatal("invented historical winner", kind)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE operation_admissions SET operation_type='clock'`); err == nil {
		t.Fatal("binding can be rewritten")
	}
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
}
