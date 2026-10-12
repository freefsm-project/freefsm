package database

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestWorkflowUpgradeRequiresReviewedMeanings(t *testing.T) {
	// The production migration runner must stop before interpreting even familiar
	// labels. Existing values are evidence, never reviewed lifecycle meanings.
	db := workflowUpgradeFixture(t)
	ctx := context.Background()
	if err := db.Migrate(ctx, MigrationFS()); err == nil {
		t.Fatal("unreviewed legacy status was silently mapped")
	}
	var name string
	if err := db.Pool.QueryRow(ctx, `SELECT name FROM statuses WHERE name='Completed'`).Scan(&name); err != nil || name != "Completed" {
		t.Fatalf("legacy evidence changed: %q %v", name, err)
	}
	inventory, err := db.WorkflowPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Pool.Query(ctx, `SELECT s.id,w.object_type FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var sid int64
		var typ string
		if err = rows.Scan(&sid, &typ); err != nil {
			t.Fatal(err)
		}
		inventory.Mappings[fmt.Sprint(sid)] = map[string]string{"job": "job:pending", "project": "project:pending", "estimate": "estimate:estimate", "invoice": "invoice:invoiced"}[typ]
	}
	rows.Close()
	var id int64
	if err = db.Pool.QueryRow(ctx, `SELECT id FROM statuses WHERE name='Completed'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	// Deliberately contrary to the label: the explicit review controls meaning.
	inventory.Mappings[fmt.Sprint(id)] = "job:pending"
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "review.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FREEFSM_WORKFLOW_MAPPING_FILE", path)
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var category string
	if err = db.Pool.QueryRow(ctx, `SELECT category_key FROM statuses WHERE id=$1`, id).Scan(&category); err != nil || category != "job:pending" {
		t.Fatalf("review ignored: %s %v", category, err)
	}
}

func TestWorkflowPreflightStopsBeforeLegacySettlementRewrites(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := migrationDatabaseThrough041(t, dsn)
	invoice := seedLegacySettlement(t, ctx, db, `[]`, `[]`, "Paid")
	if err := db.Migrate(ctx, MigrationFS()); err == nil {
		t.Fatal("unreviewed upgrade accepted")
	}
	var applied bool
	if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='042_normalized_invoice_settlement')`).Scan(&applied); err != nil || applied {
		t.Fatalf("legacy rewrite ran before review: %v %v", applied, err)
	}
	inventory, err := db.WorkflowPreflight(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.Pool.Query(ctx, `SELECT s.id,w.object_type FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		var typ string
		if err = rows.Scan(&id, &typ); err != nil {
			t.Fatal(err)
		}
		inventory.Mappings[fmt.Sprint(id)] = map[string]string{"job": "job:pending", "project": "project:pending", "estimate": "estimate:estimate", "invoice": "invoice:void"}[typ]
	}
	rows.Close()
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "review.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FREEFSM_WORKFLOW_MAPPING_FILE", path)
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var category string
	if err = db.Pool.QueryRow(ctx, `SELECT s.category_key FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.id=$1`, invoice).Scan(&category); err != nil || category != "invoice:void" {
		t.Fatalf("legacy Paid label overrode review: %s %v", category, err)
	}
}

func workflowUpgradeFixture(t *testing.T, cutoffs ...string) *DB {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	root, err := Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("workflow_upgrade_%d", time.Now().UnixNano())
	if _, err = root.Pool.Exec(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Pool.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); root.Close() })
	db, err := Connect(context.Background(), statusMigrationDSN(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	all := MigrationFS()
	cutoff := "044_"
	if len(cutoffs) > 0 {
		cutoff = cutoffs[0]
	}
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(all, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() < cutoff {
			b, err := fs.ReadFile(all, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[e.Name()] = &fstest.MapFile{Data: b}
		}
	}
	if err := db.Migrate(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestWorkflowOneShotReviewedUpgradeBeforeDefinitionSeeds(t *testing.T) {
	for _, cutoff := range []string{"032_", "013_"} {
		t.Run(cutoff, func(t *testing.T) {
			db := workflowUpgradeFixture(t, cutoff)
			ctx := context.Background()
			var workflow, custom, customer, job int64
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM status_workflows WHERE object_type='job' LIMIT 1`), &workflow)
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO statuses(workflow_id,name) VALUES($1,'Legacy dispatch meaning') RETURNING id`, workflow), &custom)
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(display_name) VALUES('Legacy workflow customer') RETURNING id`), &customer)
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(customer_id,job_type,status_id) VALUES($1,'Legacy recorded work',$2) RETURNING id`, customer, custom), &job)
			inventory, err := db.WorkflowPreflight(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rows, err := db.Pool.Query(ctx, `SELECT s.id,w.object_type FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id`)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var id int64
				var typ string
				if err = rows.Scan(&id, &typ); err != nil {
					t.Fatal(err)
				}
				inventory.Mappings[fmt.Sprint(id)] = map[string]string{"job": "job:pending", "project": "project:pending", "estimate": "estimate:estimate", "invoice": "invoice:void"}[typ]
			}
			if err = rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			data, err := json.Marshal(inventory)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "review.json")
			if err = os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FREEFSM_WORKFLOW_MAPPING_FILE", path)
			if _, err = db.Pool.Exec(ctx, `UPDATE statuses SET name='Unreviewed change' WHERE id=$1`, custom); err != nil {
				t.Fatal(err)
			}
			if err = db.Migrate(ctx, MigrationFS()); err == nil {
				t.Fatal("stale review was accepted")
			}
			var applied bool
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='032_invoice_invoiced_status')`), &applied)
			if applied {
				t.Fatal("stale review applied an intervening migration")
			}
			if _, err = db.Pool.Exec(ctx, `UPDATE statuses SET name='Legacy dispatch meaning' WHERE id=$1`, custom); err != nil {
				t.Fatal(err)
			}
			if err = db.Migrate(ctx, MigrationFS()); err != nil {
				t.Fatalf("one-shot reviewed upgrade: %v", err)
			}
			var selected, owner int64
			var category string
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT j.status_id,j.customer_id,s.category_key FROM jobs j JOIN statuses s ON s.id=j.status_id WHERE j.id=$1`, job), &selected, &owner, &category)
			if selected != custom || owner != customer || category != "job:pending" {
				t.Fatalf("record facts changed: %d %d %s", selected, owner, category)
			}
			var preserved bool
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT evidence=$1::jsonb FROM workflow_migration_evidence WHERE snapshot=$2`, inventory.Evidence, inventory.Snapshot), &preserved)
			if !preserved {
				t.Fatal("original reviewed evidence was rewritten")
			}
			var slots int
			mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(DISTINCT category_key) FROM statuses WHERE category_key LIKE 'invoice:%'`), &slots)
			if slots != 6 {
				t.Fatalf("fixed Invoice slots=%d", slots)
			}
			t.Setenv("FREEFSM_WORKFLOW_MAPPING_FILE", "")
			if err = db.Migrate(ctx, MigrationFS()); err != nil {
				t.Fatalf("restart: %v", err)
			}
		})
	}
}

func settlementMigrationFS(t *testing.T) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	all := MigrationFS()
	entries, err := fs.ReadDir(all, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() < "044_" {
			b, err := fs.ReadFile(all, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			out[e.Name()] = &fstest.MapFile{Data: b}
		}
	}
	return out
}
