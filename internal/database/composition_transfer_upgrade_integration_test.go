package database

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

// These are actual installed source horizons, not impossible schemas with
// later dependent migrations installed but their Job planning predecessor absent.
// The original standalone reviewer probe remains retained unchanged in evidence.
func TestCompositionTransferUpgradeBothInstalled075States(t *testing.T) {
	for _, source := range []string{"planning_first", "transfer_first"} {
		t.Run(source, func(t *testing.T) {
			dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
			if dsn == "" {
				t.Skip("set FREEFSM_TEST_DATABASE_URL")
			}
			db, ctx := fileMigrationDatabaseThrough046(t, dsn)
			a := seedFileOwnershipTenant(t, ctx, db, "composition-"+source)
			insertMigrationFile(t, ctx, db, a.company, a.user, "customer", a.customer)
			old := fstest.MapFS{}
			entries, err := fs.ReadDir(MigrationFS(), ".")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				name := entry.Name()
				if name >= "076" || source == "planning_first" && name == "075_file_transfer_scope.up.sql" || source == "transfer_first" && (name == "074_job_planning.up.sql" || name == "075_job_title_whitespace.up.sql") {
					continue
				}
				data, err := fs.ReadFile(MigrationFS(), name)
				if err != nil {
					t.Fatal(err)
				}
				old[name] = &fstest.MapFile{Data: data}
			}
			if err := db.Migrate(ctx, old); err != nil {
				t.Fatal(err)
			}
			const id = "85000000-0000-4000-8000-000000000001"
			if _, err := db.Pool.Exec(ctx, `INSERT INTO file_transfers(id,user_id,session_hash,publication_id,object_type,object_id,name,size,sha256,transferred) VALUES($1,$2,'recorded-client-key','85000000-0000-4000-8000-000000000002','customer',$3,'retained.txt',11,'b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9',11)`, id, a.user, a.customer); err != nil {
				t.Fatal(err)
			}
			if source == "transfer_first" {
				_, err = db.Pool.Exec(ctx, `INSERT INTO file_transfer_chunks(transfer_id,offset_bytes,data,user_id,session_hash) VALUES($1,0,$2,$3,'recorded-client-key')`, id, []byte("hello world"), a.user)
			} else {
				_, err = db.Pool.Exec(ctx, `INSERT INTO file_transfer_chunks(transfer_id,offset_bytes,data) VALUES($1,0,$2)`, id, []byte("hello world"))
			}
			if err != nil {
				t.Fatal(err)
			}
			snapshot := func() string {
				var v string
				mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT jsonb_build_object('transfers',(SELECT jsonb_agg(to_jsonb(t)) FROM file_transfers t),'chunks',(SELECT jsonb_agg(to_jsonb(c)-'user_id'-'session_hash') FROM file_transfer_chunks c),'files',(SELECT jsonb_agg(to_jsonb(f)) FROM files f),'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g))::text`), &v)
				return v
			}
			before := snapshot()
			for range 2 {
				if err := db.Migrate(ctx, MigrationFS()); err != nil {
					t.Fatal(err)
				}
				if snapshot() != before {
					t.Fatal("composed upgrade changed installed bytes, identities or grants")
				}
			}
			var names, bytes string
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT string_agg(name,',' ORDER BY name) FROM schema_migrations WHERE name LIKE '075_%'`), &names)
			if !strings.Contains(names, "075_file_transfer_scope") || !strings.Contains(names, "075_job_title_whitespace") {
				t.Fatal("migration identity lost", names)
			}
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT convert_from(c.data,'UTF8') FROM file_transfer_chunks c JOIN file_transfers t ON (t.user_id,t.session_hash,t.id)=(c.user_id,c.session_hash,c.transfer_id) WHERE t.id=$1`, id), &bytes)
			if bytes != "hello world" {
				t.Fatal("recorded bytes changed")
			}
		})
	}
}
