package database

import (
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestJobDispatchUpgradeStopsAmbiguityAndPreservesSource(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	a := seedFileOwnershipTenant(t, ctx, db, "dispatch-history")
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() >= "076_" {
			continue
		}
		data, err := fs.ReadFile(MigrationFS(), e.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[e.Name()] = &fstest.MapFile{Data: data}
	}
	if err := db.Migrate(ctx, old); err != nil {
		t.Fatal(err)
	}
	var id int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,start_time,end_time,arrival_window_start,arrival_window_end) VALUES($1,$2,'Historical interval','2020-11-01T01:30:00-05:00','2020-11-01T02:15:00-05:00','2020-11-01T04:00:00Z','2020-11-01T05:00:00Z') RETURNING id`, a.company, a.customer), &id)
	var assignmentID int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO job_assignments(job_id,user_id,role) VALUES($1,$2,'primary') RETURNING id`, id, a.user), &assignmentID)
	var disabledLegacy int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role,is_active) VALUES($1,'disabled-legacy36@test','hash','Recorded disabled assignee','tech',false) RETURNING id`, a.company), &disabledLegacy)
	if _, err := db.Pool.Exec(ctx, `UPDATE jobs SET assignments=jsonb_build_array(jsonb_build_object('user_id',$2::bigint,'name','Recorded disabled assignee')) WHERE id=$1`, id, disabledLegacy); err != nil {
		t.Fatal(err)
	}
	var before, after string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, id), &before)
	err = db.Migrate(ctx, MigrationFS())
	if err == nil || !strings.Contains(err.Error(), "dispatch requires Administrator review") {
		t.Fatalf("ambiguous window did not stop cutover: %v", err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT to_jsonb(j)::text FROM jobs j WHERE id=$1`, id), &after)
	if before != after {
		t.Fatal("failed cutover changed original facts")
	}
	// Explicit current review preserves the source, existing identifiers and the
	// selected fold instant. It records no historical overlap acknowledgment.
	sql, err := fs.ReadFile(MigrationFS(), "076_job_dispatch.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl := strings.Split(string(sql), "CREATE FUNCTION job_dispatch_source")[0]
	if _, err := db.Pool.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE users SET authorization_role_id=(SELECT id FROM authorization_roles WHERE protected),force_password_change=false WHERE id=$1`, a.user); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO job_dispatch_legacy_approvals(job_id,source,plan,reviewer_id,reason) SELECT j.id,jsonb_build_object('job',to_jsonb(j),'assignments',(SELECT jsonb_agg(to_jsonb(ja) ORDER BY ja.id) FROM job_assignments ja WHERE ja.job_id=j.id)),jsonb_set('{"start":"2020-11-01T01:30:00-05:00","end":"2020-11-01T02:15:00-05:00","arrival_start":"2020-11-01T01:00:00-05:00","arrival_end":"2020-11-01T01:45:00-05:00","due_date":"2020-11-02","assignee_ids":[]}'::jsonb,'{assignee_ids}',to_jsonb(ARRAY[$2::bigint,$3::bigint])),$2,'Administrator reviewed the original window, equal-assignee mapping and recorded standard-time instant' FROM jobs j WHERE id=$1`, id, a.user, disabledLegacy); err != nil {
		t.Fatal(err)
	}
	var grantsBefore, grantsAfter int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM authorization_grants g JOIN authorization_roles r ON r.id=g.role_id WHERE NOT r.protected`), &grantsBefore)
	if err := db.Migrate(ctx, MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var preserved bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT i.source->'job'=$2::jsonb AND j.start_time='2020-11-01T06:30:00Z'::timestamptz AND j.end_time='2020-11-01T07:15:00Z'::timestamptz AND j.arrival_window_start='2020-11-01T06:00:00Z'::timestamptz AND j.due_date='2020-11-02'::date AND NOT EXISTS(SELECT 1 FROM job_dispatch_reviews) AND NOT EXISTS(SELECT 1 FROM authorization_audit WHERE action='job.schedule') FROM jobs j JOIN job_dispatch_legacy_inventory i ON i.job_id=j.id WHERE j.id=$1`, id, before), &preserved)
	if !preserved {
		t.Fatal("reviewed mapping lost source, shifted fold instant or invented acknowledgment")
	}
	var sameAssignment bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_assignments WHERE id=$1 AND job_id=$2 AND user_id=$3 AND role='primary') AND EXISTS(SELECT 1 FROM jobs WHERE id=$2 AND assignments @> jsonb_build_array(jsonb_build_object('user_id',$3::bigint)))`, assignmentID, id, a.user), &sameAssignment)
	if !sameAssignment {
		t.Fatal("cutover lost recorded assignment identity or reviewed equal set")
	}
	var retainedUnavailable bool
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job_assignments a JOIN users u ON u.id=a.user_id WHERE a.job_id=$1 AND u.id=$2 AND NOT u.is_active)`, id, disabledLegacy), &retainedUnavailable)
	if !retainedUnavailable {
		t.Fatal("reviewed legacy disabled assignment was lost or enabled")
	}
	var newJob int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Post-cutover assignment guard') RETURNING id`, a.company, a.customer), &newJob)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, newJob, disabledLegacy); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("new disabled assignment guard not restored: %v", err)
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM authorization_grants g JOIN authorization_roles r ON r.id=g.role_id WHERE NOT r.protected`), &grantsAfter)
	if grantsAfter != grantsBefore {
		t.Fatal("upgrade granted nonprotected permissions")
	}
}
