package statusflow

import (
	"context"
	"errors"
	"testing"
)

func TestStatusIntentRenameSafeMeaningABARequiresReselection(t *testing.T) {
	db, company, admin, _, customer := statusDB(t)
	ctx := context.Background()
	var workflow, initial, job int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Jobs','job') RETURNING id`, company), &workflow)
	if _, err := db.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) SELECT $1,$2,label,'job:'||key,1,true FROM (VALUES ('new','New'),('travel_time','Travel'),('in_progress','Working'),('pending','Pending'),('completed','Completed'),('canceled','Canceled')) v(key,label)`, company, workflow); err != nil {
		t.Fatal(err)
	}
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='job:new'`, workflow), &initial)
	mustScan(t, db.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,status_id) VALUES($1,$2,'Work',$3) RETURNING id`, company, customer, initial), &job)
	svc := New(db)
	actor := Actor{ID: admin, CompanyID: company}
	target, err := svc.Create(ctx, actor, CreateRequest{Type: Job, Name: "Awaiting parts", Category: JobPending})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := svc.ObserveIntent(ctx, company, Job, job, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Update(ctx, actor, target.ID, "Awaiting delivery", "#112233"); err != nil {
		t.Fatal(err)
	}
	if err = svc.ApplyIntent(ctx, actor, Job, job, intent); err != nil {
		t.Fatalf("rename rejected: %v", err)
	}
	if err = svc.ApplyIntent(ctx, actor, Job, job, intent); err != nil {
		t.Fatalf("same target no-op rejected: %v", err)
	}
	if err = svc.TransitionJob(ctx, actor, job, initial); err != nil {
		t.Fatal(err)
	}
	if err = svc.ApplyIntent(ctx, actor, Job, job, intent); !errors.Is(err, ErrStaleIntent) {
		t.Fatalf("record ABA accepted: %v", err)
	}
	intent, err = svc.ObserveIntent(ctx, company, Job, job, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Move(ctx, actor, MoveRequest{StatusID: target.ID, Category: JobInProgress}); err != nil {
		t.Fatal(err)
	}
	if err = svc.Move(ctx, actor, MoveRequest{StatusID: target.ID, Category: JobPending}); err != nil {
		t.Fatal(err)
	}
	if err = svc.ApplyIntent(ctx, actor, Job, job, intent); !errors.Is(err, ErrStaleIntent) {
		t.Fatalf("meaning ABA accepted: %v", err)
	}
}

func TestWorkflowDeletionRacesNewReferenceWithoutLosingSelection(t *testing.T) {
	db, company, admin, _, customer := statusDB(t)
	ctx := context.Background()
	var workflow, initial int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Jobs','job') RETURNING id`, company), &workflow)
	if _, err := db.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) SELECT $1,$2,label,'job:'||key,1,true FROM (VALUES ('new','New'),('travel_time','Travel'),('in_progress','Working'),('pending','Pending'),('completed','Completed'),('canceled','Canceled')) v(key,label)`, company, workflow); err != nil {
		t.Fatal(err)
	}
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='job:new'`, workflow), &initial)
	svc := New(db)
	actor := Actor{ID: admin, CompanyID: company}
	replacement, err := svc.Create(ctx, actor, CreateRequest{Type: Job, Name: "Replacement", Category: JobNew})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.SetDefault(ctx, actor, replacement.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.SetDefault(ctx, actor, initial); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var job int64
	mustScan(t, tx.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,status_id) VALUES($1,$2,'Racing reference',$3) RETURNING id`, company, customer, initial), &job)
	done := make(chan error, 1)
	go func() { done <- svc.Delete(ctx, actor, initial, replacement.ID) }()
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	intent, err := svc.ObserveIntent(ctx, company, Job, job, replacement.ID)
	if err != nil || intent.StatusID != replacement.ID {
		t.Fatalf("reference lost: %+v %v", intent, err)
	}
	key, err := svc.CurrentCategory(ctx, company, Job, job)
	if err != nil || key != JobNew {
		t.Fatalf("meaning lost: %s %v", key, err)
	}
}
