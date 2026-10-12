package database

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestCustomerMasterPreflightAndUpgradePreserveAmbiguousFacts(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := activityMigrationDatabaseThrough048(t, dsn)
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() < "058_" {
			b, err := fs.ReadFile(MigrationFS(), entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[entry.Name()] = &fstest.MapFile{Data: b}
		}
	}
	if err = db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, `INSERT INTO customers(id,company_id,display_name,account_type,company_name) SELECT 701,id,'Same','individual','Ambiguous organization' FROM companies LIMIT 1;
 INSERT INTO customer_contacts(id,customer_id,company_id,first_name,email) SELECT 702,701,company_id,'Same','same@test' FROM customers WHERE id=701;
 INSERT INTO customer_contacts(id,customer_id,company_id,first_name,email) SELECT 703,701,company_id,'Same','same@test' FROM customers WHERE id=701;
 INSERT INTO locations(id,object_type,object_id,company_id,title,is_primary) SELECT x,'customer',701,company_id,'Same place',true FROM customers CROSS JOIN unnest(ARRAY[704,705]) x WHERE id=701;`)
	if err != nil {
		t.Fatal(err)
	}
	var before string
	if err = db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(l) ORDER BY id)::text FROM locations l`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	report, err := db.CustomerMasterPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(report, "duplicate_primary") || !strings.Contains(report, "kind_review") || !strings.Contains(report, "ownership_history_unrecorded") {
		t.Fatalf("missing explicit review findings: %s", report)
	}
	var after string
	if err = db.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(l) ORDER BY id)::text FROM locations l`).Scan(&after); err != nil || before != after {
		t.Fatal("preflight mutated facts", err)
	}
	if err = db.Migrate(ctx, MigrationFS()); err == nil {
		t.Fatal("unreviewed duplicate primaries accepted")
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE locations SET is_primary=false WHERE id=705`); err != nil {
		t.Fatal(err)
	}
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM customer_contacts WHERE id IN (702,703) AND customer_id=701 AND email='same@test'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("valid duplicate people collapsed: %d %v", count, err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE customer_contacts SET customer_id=999 WHERE id=702`); err == nil {
		t.Fatal("ownership became mutable")
	}
}
