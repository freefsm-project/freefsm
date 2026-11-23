package exports

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func TestRecoverySelectionExpiredRechecksEveryOriginalRow(t *testing.T) {
	pool, actor, company := operationalExportDB(t)
	ctx := context.Background()
	var customer, other int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Recover one'),($1,$2,'Recover two')`, company, customer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'recovery98@test','Other requester','hash','tech') RETURNING id`, company).Scan(&other); err != nil {
		t.Fatal(err)
	}
	svc := Product(pool)
	options := json.RawMessage(`{"search":"Recover","sort":"schedule_desc"}`)
	id, err := svc.Accept(ctx, actor, uuid.NewString(), "job-operational-A-v1", options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '25 hours' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if d, err := svc.PrepareDownload(ctx, actor, id); !errors.Is(err, ErrUnavailable) || d != nil {
		if d != nil {
			_ = d.Close()
		}
		t.Fatalf("expired download: %v", err)
	}
	recovered, err := svc.RecoverySelection(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	var f authorization.JobOperationalFilter
	if err := json.Unmarshal(recovered.Options, &f); err != nil {
		t.Fatal(err)
	}
	if recovered.Kind != "job-operational-A-v1" || f.Search != "Recover" || f.Sort != "schedule_desc" {
		t.Fatalf("lost intent: %+v %+v", recovered, f)
	}
	denied, err := svc.RecoverySelection(ctx, authorization.Actor{UserID: other}, id)
	if !errors.Is(err, authorization.ErrNotFound) || denied.Kind != "" || len(denied.Options) != 0 {
		t.Fatalf("requester leak: %+v %v", denied, err)
	}
	// Losing just the last stored row must hide ALL filter metadata, even expired.
	if _, err := pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=(SELECT min(id) FROM jobs)`); err != nil {
		t.Fatal(err)
	}
	denied, err = svc.RecoverySelection(ctx, actor, id)
	if !errors.Is(err, ErrUnavailable) || denied.Kind != "" || len(denied.Options) != 0 {
		t.Fatalf("lost row leaked selection: %+v %v", denied, err)
	}
}

func TestRecoverySelectionFailedStillChecksSelectedInputs(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	ctx := context.Background()
	var customer, reader int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'failed-recovery98@test','Reader','hash','tech') RETURNING id`, company).Scan(&reader); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, admin, 0, "Failed recovery", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.export", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, admin, reader, role); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: reader}
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Selected empty customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	options, err := json.Marshal(authorization.DocumentListFilter{CustomerID: customer, Search: "Retained search"})
	if err != nil {
		t.Fatal(err)
	}
	svc := Product(pool)
	id, err := svc.Accept(ctx, actor, uuid.NewString(), "estimate-list-A-v1", options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_recovery98() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected preparation failure'; END $$; CREATE TRIGGER fail_recovery98 BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION fail_recovery98()`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err == nil {
		t.Fatal("expected generation failure")
	}
	recovered, err := svc.RecoverySelection(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	var f authorization.DocumentListFilter
	if err := json.Unmarshal(recovered.Options, &f); err != nil {
		t.Fatal(err)
	}
	if recovered.Kind != "estimate-list-A-v1" || f.CustomerID != customer || f.Search != "Retained search" {
		t.Fatalf("lost failed intent: %+v", recovered)
	}
	if _, err := pool.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, customer); err != nil {
		t.Fatal(err)
	}
	denied, err := svc.RecoverySelection(ctx, actor, id)
	if !errors.Is(err, ErrUnavailable) || denied.Kind != "" || len(denied.Options) != 0 {
		t.Fatalf("failed selection leak: %+v %v", denied, err)
	}
}

func TestRecoverySelectionBlockedAndRevokedEmptyCohort(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	ctx := context.Background()
	var reader int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'recover-cohort98@test','Reader','hash','tech') RETURNING id`, company).Scan(&reader); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.export", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.export", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, admin, 0, "Recovery cohort", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, admin, reader, role); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: reader}
	svc := Product(pool)
	id, err := svc.Accept(ctx, actor, uuid.NewString(), "estimate-cohort-A-v1", json.RawMessage(`{"created_from":"2026-01-01"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET blocked=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	denied, err := svc.RecoverySelection(ctx, actor, id)
	if !errors.Is(err, ErrUnavailable) || denied.Kind != "" || len(denied.Options) != 0 {
		t.Fatalf("blocked metadata: %+v %v", denied, err)
	}
	// A separate empty artifact proves authority is checked independently of rows.
	second, err := svc.Accept(ctx, actor, uuid.NewString(), "estimate-cohort-A-v1", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, admin, role, "Recovery cohort", grants[:3], revision); err != nil {
		t.Fatal(err)
	}
	denied, err = svc.RecoverySelection(ctx, actor, second)
	if !errors.Is(err, ErrUnavailable) || denied.Kind != "" || len(denied.Options) != 0 {
		t.Fatalf("revoked Invoice Export metadata: %+v %v", denied, err)
	}
}
