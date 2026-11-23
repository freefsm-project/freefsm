package authorization_test

import (
	"context"
	"errors"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestWorkloadOperationalSnapshotScopeFiltersAndReferences(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var user, company int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'workload@test','Workload admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id,company_id`).Scan(&user, &company); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: user}
	// Initial protected administrator is bootstrapped by the existing service.
	p, err := svc.Principal(ctx, actor)
	if err != nil {
		t.Fatal(err)
	}
	var customer, job, other, disabled int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Report customer') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,is_active) VALUES($1,'retained@test','Retained','hash','tech',true) RETURNING id`, company).Scan(&disabled); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,subtitle) VALUES($1,$2,'Matching title','needle') RETURNING id`, company, customer).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Other') RETURNING id`, company, customer).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2),($1,$3)`, job, user, disabled); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, disabled); err != nil {
		t.Fatal(err)
	}
	ui, err := svc.JobOperational(ctx, actor, authorization.JobOperationalFilter{}, authorization.All)
	if err != nil || len(ui.UIBillingTypes) != 2 {
		t.Fatalf("authorized UI billing facet: %+v %v", ui, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	report, err := authorization.WorkloadSnapshot(ctx, tx, p, authorization.WorkloadFilter{AssigneeIDs: []int64{disabled}}, authorization.Assigned)
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.Total != 1 || report.Totals.Unscheduled != 1 || len(report.Groups) != 1 || report.Groups[0].ID != disabled || len(report.Rows[0].Assignees) != 2 || report.Rows[0].Assignees[1] != "Retained (unavailable)" {
		t.Fatalf("group/row bounds: %+v", report)
	}
	all, err := authorization.WorkloadSnapshot(ctx, tx, p, authorization.WorkloadFilter{}, authorization.All)
	if err != nil || all.Totals.Total != 2 {
		t.Fatalf("all %+v %v", all, err)
	}
	assigned, err := authorization.JobOperationalSnapshot(ctx, tx, p, authorization.JobOperationalFilter{}, authorization.Assigned)
	if err != nil || len(assigned.Rows) != 1 || assigned.Rows[0].ID != job {
		t.Fatalf("Assigned widened by broad view: %+v %v", assigned, err)
	}
	if assigned.UIBillingTypes != nil {
		t.Fatal("raw operational selector included pricing")
	}
	search, err := authorization.JobOperationalSnapshot(ctx, tx, p, authorization.JobOperationalFilter{Search: "NEEDLE"}, authorization.All)
	if err != nil || len(search.Rows) != 1 {
		t.Fatalf("subtitle search %+v %v", search, err)
	}
	var status int64
	if err := tx.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:new' LIMIT 1`, company).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET status_id=$1,created_at='2026-01-01T00:00:00Z' WHERE id IN ($2,$3)`, status, job, other); err != nil {
		t.Fatal(err)
	}
	ordered, err := authorization.JobOperationalSnapshot(ctx, tx, p, authorization.JobOperationalFilter{StatusID: status, CustomerID: customer}, authorization.All)
	if err != nil || len(ordered.Rows) != 2 || ordered.Rows[0].ID != other || ordered.Rows[1].ID != job {
		t.Fatalf("status/customer selection and ID tie-break: %+v %v", ordered, err)
	}
	if _, err := authorization.JobOperationalSnapshot(ctx, tx, p, authorization.JobOperationalFilter{CustomerID: 999999, Search: "empty"}, authorization.All); err == nil {
		t.Fatal("inaccessible selected filter accepted on empty results")
	}
	ref := assigned.Rows[0].Reference
	if err := authorization.CheckJobOperationalReference(ctx, tx, p, ref, authorization.Assigned); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job, user); err != nil {
		t.Fatal(err)
	}
	if err := authorization.CheckJobOperationalReference(ctx, tx, p, ref, authorization.Assigned); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("assignment loss accepted: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET deleted_at=now() WHERE id=$1`, job); err != nil {
		t.Fatal(err)
	}
	if err := authorization.CheckJobOperationalReference(ctx, tx, p, ref, authorization.All); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("archive accepted: %v", err)
	}
}

func TestWorkloadOperationalContextAndOriginalCustomerRecheck(t *testing.T) {
	pool, svc := fixture(t)
	ctx := context.Background()
	var admin, company, user, customer, nextCustomer, job, sibling int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'workload-admin@test','Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id,company_id`).Scan(&admin, &company); err != nil {
		t.Fatal(err)
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Report operator", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.export", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,'workload-user@test','Viewer','hash','tech',$2) RETURNING id`, company, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []*int64{&customer, &nextCustomer} {
		if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, company).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	for _, dest := range []*int64{&job, &sibling} {
		if err := pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Title') RETURNING id`, company, customer).Scan(dest); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, *dest, user); err != nil {
			t.Fatal(err)
		}
	}
	p, err := svc.Principal(ctx, authorization.Actor{UserID: user})
	if err != nil {
		t.Fatal(err)
	}
	if err := authorization.AuthorizeJobOperationalExport(p, authorization.All); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("Assigned export widened: %v", err)
	}
	ui, err := svc.JobOperational(ctx, authorization.Actor{UserID: user}, authorization.JobOperationalFilter{}, authorization.All)
	if err != nil || len(ui.UIBillingTypes) != 0 || len(ui.Rows) != 2 {
		t.Fatalf("UI billing without independent pricing grant: %+v %v", ui, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	r, err := authorization.JobOperationalSnapshot(ctx, tx, p, authorization.JobOperationalFilter{CustomerID: customer}, authorization.Assigned)
	if err != nil || len(r.Rows) != 2 {
		t.Fatalf("Context selected customer %+v %v", r, err)
	}
	ref := authorization.JobOperationalReference{JobID: job, CustomerID: customer}
	if err := authorization.CheckJobOperationalReference(ctx, tx, p, ref, authorization.Assigned); err != nil {
		t.Fatalf("Context reference: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE jobs SET customer_id=$1 WHERE id=$2`, nextCustomer, job); err != nil {
		t.Fatal(err)
	}
	// A sibling still grants general context for the old Customer, but cannot
	// authorize an artifact whose original first link was this moved Job.
	if err := authorization.CheckJobOperationalReference(ctx, tx, p, ref, authorization.Assigned); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("old first-link loss accepted: %v", err)
	}
	if err := authorization.CheckJobOperationalReference(ctx, tx, p, authorization.JobOperationalReference{JobID: job, CustomerID: nextCustomer}, authorization.Assigned); err != nil {
		t.Fatalf("new current context denied: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, nextCustomer); err != nil {
		t.Fatal(err)
	}
	if err := authorization.CheckJobOperationalReference(ctx, tx, p, authorization.JobOperationalReference{JobID: job, CustomerID: nextCustomer}, authorization.Assigned); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("archived context accepted: %v", err)
	}
}
