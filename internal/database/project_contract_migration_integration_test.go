package database

import (
	"io/fs"
	"os"
	"testing"
	"testing/fstest"
)

func TestProjectUpgradePreservesFactsAndReportsUnknownProgress(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	a := seedFileOwnershipTenant(t, ctx, db, "project-history")
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
	if err = db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO company_settings(company_id,timezone) VALUES($1,'Pacific/Auckland')`, a.company); err != nil {
		t.Fatal(err)
	}
	var workflow, completed int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Project fixture','project') RETURNING id`, a.company), &workflow)
	if _, err = db.Pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) SELECT $1,$2,k,'project:'||k,1,true FROM unnest(ARRAY['new','in_progress','pending','completed','canceled']) k`, a.company, workflow); err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='project:completed'`, workflow), &completed)
	var id int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO projects(company_id,customer_id,name,completion_percentage,start_time,end_time,status_id)
 VALUES($1,$2,'Recorded legacy Project',100,'2025-01-01T11:00:00Z','2025-01-03T11:00:00Z',
 $3) RETURNING id`, a.company, a.customer, completed).Scan(&id); err != nil {
		t.Fatal(err)
	}
	var before, after []byte
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(p) FROM projects p WHERE id=$1`, id), &before)
	if err = db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(p)-'planned_start_date'-'planned_end_date'-'service_notes' FROM projects p WHERE id=$1`, id), &after)
	if string(before) != string(after) {
		t.Fatalf("recorded facts changed: %s -> %s", before, after)
	}
	var preserved bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT recorded=$2::jsonb AND 'prior non-completed manual progress is unknown; recorded 100 is not evidence'=ANY(review_reasons) FROM project_legacy_review WHERE project_id=$1`, id, string(before)), &preserved)
	if !preserved {
		t.Fatal("unknown progress was not reported with original evidence")
	}
	var start, end string
	if err = db.Pool.QueryRow(ctx, `SELECT planned_start_date::text,planned_end_date::text FROM projects WHERE id=$1`, id).Scan(&start, &end); err != nil {
		t.Fatal(err)
	}
	if start != "2025-01-02" || end != "2025-01-04" {
		t.Fatalf("local dates = %s / %s", start, end)
	}
}
