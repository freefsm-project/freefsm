package database

import (
	"os"
	"strings"
	"testing"
)

func TestJobFieldsLegacyUpgradePreservesFactsAndStopsUnreviewedMappings(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	for _, kind := range []string{"valid", "missing_mapping", "invalid_type"} {
		t.Run(kind, func(t *testing.T) {
			db, ctx := activityMigrationDatabaseThrough048(t, dsn)
			if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role) SELECT 4101,id,'legacy-fields@test','Recorded worker','hash','tech' FROM companies LIMIT 1;
INSERT INTO customers(id,company_id,display_name) SELECT 4101,id,'Recorded customer' FROM companies LIMIT 1;
INSERT INTO custom_field_definitions(id,object_type,name,field_type,options) VALUES(4101,'job','Recorded choice','select','["Exact OLD identity","Other"]');
INSERT INTO jobs(id,company_id,customer_id,job_type,custom_fields) SELECT 4101,id,4101,'Recorded Job','[{"definition_id":4101,"value":"Exact OLD identity"}]' FROM companies LIMIT 1;
INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata,created_at) SELECT id,4101,'update','job',4101,'{"recorded_field":"Exact OLD identity"}','2020-01-02T03:04:05Z' FROM companies LIMIT 1;`); err != nil {
				t.Fatal(err)
			}
			if kind == "missing_mapping" {
				if _, err := db.Pool.Exec(ctx, `UPDATE jobs SET custom_fields='[{"definition_id":4999,"value":"Never guess"}]' WHERE id=4101`); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "invalid_type" {
				if _, err := db.Pool.Exec(ctx, `UPDATE jobs SET custom_fields='[{"definition_id":4101,"value":true}]' WHERE id=4101`); err != nil {
					t.Fatal(err)
				}
			}
			var before, after, historyBefore, historyAfter string
			if err := db.Pool.QueryRow(ctx, `SELECT custom_fields::text FROM jobs WHERE id=4101`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			const history = `SELECT jsonb_build_array(actor_id,action,object_type,object_id,metadata,created_at)::text FROM activity_logs WHERE object_type='job' AND object_id=4101`
			if err := db.Pool.QueryRow(ctx, history).Scan(&historyBefore); err != nil {
				t.Fatal(err)
			}
			err := db.Migrate(ctx, MigrationFS())
			if kind == "valid" && err != nil {
				t.Fatal(err)
			}
			if kind != "valid" && (err == nil || !strings.Contains(err.Error(), "review")) {
				t.Fatalf("unreviewed upgrade did not stop: %v", err)
			}
			if err := db.Pool.QueryRow(ctx, `SELECT custom_fields::text FROM jobs WHERE id=4101`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatalf("history coerced: %s -> %s", before, after)
			}
			if err := db.Pool.QueryRow(ctx, history).Scan(&historyAfter); err != nil {
				t.Fatal(err)
			}
			if historyBefore != historyAfter {
				t.Fatalf("known attribution rewritten: %s -> %s", historyBefore, historyAfter)
			}
			if kind == "valid" {
				var nonprotected, admin, invented int
				if err := db.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE NOT r.protected),count(*) FILTER(WHERE r.protected) FROM authorization_grants g JOIN authorization_roles r ON r.id=g.role_id WHERE g.permission='job.fields.edit'`).Scan(&nonprotected, &admin); err != nil {
					t.Fatal(err)
				}
				if nonprotected != 0 || admin != 1 {
					t.Fatalf("upgrade auto-granted fields: nonprotected=%d protected=%d", nonprotected, admin)
				}
				if err := db.Pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='job.fields.edit'`).Scan(&invented); err != nil {
					t.Fatal(err)
				}
				if invented != 0 {
					t.Fatal("upgrade invented field revision attribution")
				}
				if err := db.Migrate(ctx, MigrationFS()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
