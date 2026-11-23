package exports

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func TestWorkloadDurableDistinctJobsAndFlags(t *testing.T) {
	pool, actor, company := operationalExportDB(t)
	ctx := context.Background()
	var customer, job, other int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,due_date) VALUES($1,$2,'Unscheduled overdue',CURRENT_DATE-10) RETURNING id`, company, customer).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'other98@test','Second assignee','hash','tech') RETURNING id`, company).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2),($1,$3)`, job, actor.UserID, other); err != nil {
		t.Fatal(err)
	}
	svc := Product(pool)
	id, err := svc.Accept(ctx, actor, uuid.NewString(), "workload-A-v1", json.RawMessage(`{"measure":"unscheduled,overdue,overdue"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	download, err := svc.PrepareDownload(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(download).ReadAll()
	_ = download.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("multi-assignee Job duplicated: %v", rows)
	}
	r := rows[1]
	if r[7] != "Exporter; Second assignee" || r[11] != "false" || r[12] != "true" || r[13] != "false" || r[14] != "true" || r[15] != "false" {
		t.Fatalf("assignees/measures: %v", r)
	}
	var asOf string
	if err := pool.QueryRow(ctx, `SELECT as_of::text FROM export_jobs WHERE id=$1`, id).Scan(&asOf); err != nil {
		t.Fatal(err)
	}
	if r[0] == "" || r[1] == "" || asOf == "" {
		t.Fatal("missing shared snapshot clock")
	}
}

func TestOperationalExportsSelectedInputDenialAndFailedPublication(t *testing.T) {
	pool, actor, company := operationalExportDB(t)
	ctx := context.Background()
	svc := Product(pool)
	for _, kind := range []string{"job-operational-A-v1", "workload-A-v1", "estimate-list-A-v1", "invoice-list-A-v1", "estimate-cohort-A-v1"} {
		options := json.RawMessage(`{"customer_id":9223372036854775807}`)
		if _, err := svc.Accept(ctx, actor, uuid.NewString(), kind, options); !errors.Is(err, authorization.ErrNotFound) && !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("%s empty selected-input denial: %v", kind, err)
		}
	}
	var customer int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'First'),($1,$2,'Second')`, company, customer); err != nil {
		t.Fatal(err)
	}
	id, err := svc.Accept(ctx, actor, uuid.NewString(), "workload-A-v1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_export98() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal=2 THEN RAISE EXCEPTION 'injected storage failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_export98 BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION fail_export98()`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err == nil {
		t.Fatal("publication unexpectedly succeeded")
	}
	var state string
	var records int
	if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1) FROM export_jobs WHERE id=$1`, id).Scan(&state, &records); err != nil {
		t.Fatal(err)
	}
	if state != "Failed" || records != 0 {
		t.Fatalf("partial artifact: %s %d", state, records)
	}
	if d, err := svc.PrepareDownload(ctx, actor, id); !errors.Is(err, ErrNotReady) || d != nil {
		if d != nil {
			_ = d.Close()
		}
		t.Fatalf("failed artifact download: %v", err)
	}
}
