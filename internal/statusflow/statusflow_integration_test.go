package statusflow

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTransitionAuthorizationAuditAndCategorySemanticsIntegration(t *testing.T) {
	db, company, admin, tech, customer := statusDB(t)
	ctx := context.Background()
	var workflow, newID, doneID, job int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Jobs','job') RETURNING id`, company), &workflow)
	if _, err := db.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
	 ($1,$2,'Renamed start','job:new',1,true),($1,$2,'Renamed finish','job:completed',1,true),
	 ($1,$2,'Travel','job:travel_time',1,true),($1,$2,'Working','job:in_progress',1,true),
	 ($1,$2,'Waiting','job:pending',1,true),($1,$2,'Stopped','job:canceled',1,true)`, company, workflow); err != nil {
		t.Fatal(err)
	}
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='job:new'`, workflow), &newID)
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='job:completed'`, workflow), &doneID)
	mustScan(t, db.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,status_id) VALUES($1,$2,'Work',$3) RETURNING id`, company, customer, newID), &job)
	svc := New(db)
	actor := Actor{ID: admin, CompanyID: company, Role: "admin"}
	if err := svc.TransitionJob(ctx, actor, job, doneID); err != nil {
		t.Fatal(err)
	}
	var category string
	var logs int
	mustScan(t, db.QueryRow(ctx, `SELECT s.category_key FROM jobs j JOIN statuses s ON s.id=j.status_id WHERE j.id=$1`, job), &category)
	mustScan(t, db.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='job' AND object_id=$1 AND action='status_transitioned'`, job), &logs)
	if category != "job:completed" || logs != 1 {
		t.Fatalf("category=%s logs=%d", category, logs)
	}
	if err := svc.TransitionJob(ctx, Actor{ID: tech, CompanyID: company, Role: "tech"}, job, newID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unassigned tech error=%v", err)
	}
	if err := svc.TransitionJob(ctx, Actor{ID: tech, CompanyID: company, Role: "admin"}, job, newID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("spoofed admin transition error=%v", err)
	}
	if _, err := svc.Create(ctx, Actor{ID: tech, CompanyID: company, Role: "admin"}, CreateRequest{Type: Job, Name: "Spoof", Category: JobPending}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("spoofed admin config error=%v", err)
	}
	mustScan(t, db.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='job' AND object_id=$1`, job), &logs)
	if logs != 1 {
		t.Fatalf("failed transition wrote audit: %d", logs)
	}
}

func TestMoveAndDeleteReassignHistoricalRecordsIntegration(t *testing.T) {
	db, company, admin, _, customer := statusDB(t)
	ctx := context.Background()
	var workflow int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Jobs','job') RETURNING id`, company), &workflow)
	if _, err := db.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
	 ($1,$2,'New','job:new',1,true),($1,$2,'Travel','job:travel_time',1,true),($1,$2,'Working','job:in_progress',1,true),
	 ($1,$2,'Old pending','job:pending',1,true),($1,$2,'Done','job:completed',1,true),($1,$2,'Canceled','job:canceled',1,true)`, company, workflow); err != nil {
		t.Fatal(err)
	}
	svc := New(db)
	actor := Actor{ID: admin, CompanyID: company, Role: "ignored"}
	replacement, err := svc.Create(ctx, actor, CreateRequest{Type: Job, Name: "Replacement pending", Category: JobPending})
	if err != nil {
		t.Fatal(err)
	}
	var old int64
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND name='Old pending'`, workflow), &old)
	var job int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,status_id,deleted_at) VALUES($1,$2,'Archived',$3,now()) RETURNING id`, company, customer, old), &job)
	if err = svc.Delete(ctx, actor, old, replacement.ID); err != nil {
		t.Fatal(err)
	}
	var assigned int64
	mustScan(t, db.QueryRow(ctx, `SELECT status_id FROM jobs WHERE id=$1`, job), &assigned)
	if assigned != replacement.ID {
		t.Fatalf("archived assignment=%d want %d", assigned, replacement.ID)
	}
	var defaulted bool
	mustScan(t, db.QueryRow(ctx, `SELECT is_category_default FROM statuses WHERE id=$1`, replacement.ID), &defaulted)
	if !defaulted {
		t.Fatal("replacement was not promoted")
	}
	moving, err := svc.Create(ctx, actor, CreateRequest{Type: Job, Name: "Move me", Category: JobPending})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Move(ctx, actor, MoveRequest{StatusID: moving.ID, Category: JobCanceled, Order: -10}); err != nil {
		t.Fatal(err)
	}
	var key string
	var order int
	mustScan(t, db.QueryRow(ctx, `SELECT category_key,category_order FROM statuses WHERE id=$1`, moving.ID), &key, &order)
	if key != "job:canceled" || order != 1 {
		t.Fatalf("moved key=%s order=%d", key, order)
	}
	var configLogs int
	mustScan(t, db.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='status' AND action IN ('status_deleted','status_moved')`), &configLogs)
	if configLogs != 2 {
		t.Fatalf("config logs=%d", configLogs)
	}
}

func TestEffectiveInvoiceStatusDecisionTableIntegration(t *testing.T) {
	db, company, admin, _, customer := statusDB(t)
	ctx := context.Background()
	var workflow int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoices','invoice') RETURNING id`, company), &workflow)
	keys := []CategoryKey{InvoiceDraft, InvoiceInvoiced, InvoiceSent, InvoicePartiallyPaid, InvoicePaid, InvoiceVoid}
	ids := map[CategoryKey]int64{}
	if _, err := db.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
	 ($1,$2,'Slot 0','invoice:draft',1,true),($1,$2,'Slot 1','invoice:invoiced',1,true),($1,$2,'Slot 2','invoice:sent',1,true),
	 ($1,$2,'Slot 3','invoice:partially_paid',1,true),($1,$2,'Slot 4','invoice:paid',1,true),($1,$2,'Slot 5','invoice:void',1,true)`, company, workflow); err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		var id int64
		mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key=$2`, workflow, key), &id)
		ids[key] = id
	}
	var invoice int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,status_id,invoice_number,title,line_items,settlement_state) VALUES($1,$2,$3,1,'Invoice','[]','paid') RETURNING id`, company, customer, ids[InvoiceDraft]), &invoice)
	got, err := New(db).EffectiveInvoiceStatus(ctx, company, invoice)
	if err != nil {
		t.Fatal(err)
	}
	if got.Category != InvoiceDraft {
		t.Fatalf("zero-total draft effective=%s", got.Category)
	}
	if _, err = db.Exec(ctx, `UPDATE invoices SET status_id=$1 WHERE id=$2`, ids[InvoiceSent], invoice); err == nil {
		t.Fatal("direct invoice status bypass succeeded")
	}
	if err = New(db).TransitionInvoice(ctx, Actor{ID: admin, CompanyID: company, Role: "admin"}, invoice, ids[InvoiceSent]); err != nil {
		t.Fatal(err)
	}
	got, err = New(db).EffectiveInvoiceStatus(ctx, company, invoice)
	if err != nil || got.Category != InvoicePaid {
		t.Fatalf("manual sent paid effective=%s err=%v", got.Category, err)
	}
	if err := New(db).TransitionInvoice(ctx, Actor{ID: admin, CompanyID: company, Role: "admin"}, invoice, ids[InvoicePartiallyPaid]); !errors.Is(err, ErrPaymentDerived) {
		t.Fatalf("payment slot transition=%v", err)
	}
	actor := Actor{ID: admin, CompanyID: company, Role: "admin"}
	if err := New(db).TransitionInvoice(ctx, actor, invoice, ids[InvoiceVoid]); err != nil {
		t.Fatalf("void zero-total invoice: %v", err)
	}
	if err := New(db).TransitionInvoice(ctx, actor, invoice, ids[InvoiceSent]); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("void to sent transition=%v", err)
	}
	if err := New(db).TransitionInvoice(ctx, actor, invoice, ids[InvoiceDraft]); err != nil {
		t.Fatalf("return void invoice to draft: %v", err)
	}
}

func TestSetDefaultRejectsInvoiceWithoutMutationIntegration(t *testing.T) {
	db, company, admin, _, _ := statusDB(t)
	ctx := context.Background()
	var workflow, currentID int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoices','invoice') RETURNING id`, company), &workflow)
	if _, err := db.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
		($1,$2,'Current','invoice:draft',1,true),($1,$2,'Invoiced','invoice:invoiced',1,true),($1,$2,'Sent','invoice:sent',1,true),
		($1,$2,'Partially paid','invoice:partially_paid',1,true),($1,$2,'Paid','invoice:paid',1,true),
		($1,$2,'Void','invoice:void',1,true)`, company, workflow); err != nil {
		t.Fatal(err)
	}
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND name='Current'`, workflow), &currentID)

	err := New(db).SetDefault(ctx, Actor{ID: admin, CompanyID: company}, currentID)
	if !errors.Is(err, ErrWrongType) {
		t.Fatalf("SetDefault invoice error = %v, want ErrWrongType", err)
	}
	var currentDefault bool
	var activityCount int
	mustScan(t, db.QueryRow(ctx, `SELECT is_category_default FROM statuses WHERE id=$1`, currentID), &currentDefault)
	mustScan(t, db.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='status' AND object_id=$1 AND action='status_default_changed'`, currentID), &activityCount)
	if !currentDefault || activityCount != 0 {
		t.Fatalf("invoice default mutation persisted: default=%t activity=%d", currentDefault, activityCount)
	}
}

func TestFinalizeInvoiceUsesRenamedInvoicedCategoryDefaultAtomicallyIntegration(t *testing.T) {
	db, company, admin, _, customer := statusDB(t)
	ctx := context.Background()
	var workflow, draftID, invoiceID int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoices','invoice') RETURNING id`, company), &workflow)
	if _, err := db.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
	 ($1,$2,'Working copy','invoice:draft',1,true),($1,$2,'Approved for billing','invoice:invoiced',1,true),
	 ($1,$2,'Customer copy','invoice:sent',1,true),($1,$2,'Some funds','invoice:partially_paid',1,true),
	 ($1,$2,'Settled','invoice:paid',1,true),($1,$2,'Withdrawn','invoice:void',1,true)`, company, workflow); err != nil {
		t.Fatal(err)
	}
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='invoice:draft'`, workflow), &draftID)
	mustScan(t, db.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,status_id,invoice_number,title,line_items,settlement_state) VALUES($1,$2,$3,1,'Invoice','[]','paid') RETURNING id`, company, customer, draftID), &invoiceID)
	date := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	if err := New(db).FinalizeInvoice(ctx, Actor{ID: admin, CompanyID: company}, invoiceID, date, 14); err != nil {
		t.Fatal(err)
	}
	var category string
	var invoiceDate, dueDate time.Time
	var logs int
	mustScan(t, db.QueryRow(ctx, `SELECT s.category_key,i.invoice_date,i.due_date FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.id=$1`, invoiceID), &category, &invoiceDate, &dueDate)
	mustScan(t, db.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='invoice' AND object_id=$1 AND action='status_transitioned'`, invoiceID), &logs)
	if category != "invoice:invoiced" || !invoiceDate.Equal(date) || !dueDate.Equal(date.AddDate(0, 0, 14)) || logs != 1 {
		t.Fatalf("category=%s invoice=%s due=%s logs=%d", category, invoiceDate, dueDate, logs)
	}
}

func TestAcceptanceHookTransitionsToConfiguredSentDefaultOnlyWhenStatusUnchangedIntegration(t *testing.T) {
	db, company, admin, _, customer := statusDB(t)
	ctx := context.Background()
	var workflow, draftID, sentID, otherID, estimateID int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Estimates','estimate') RETURNING id`, company), &workflow)
	if _, err := db.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
	 ($1,$2,'Proposal prep','estimate:draft',1,true),($1,$2,'Proposal','estimate:estimate',1,true),
	 ($1,$2,'Customer copy','estimate:sent',1,true),($1,$2,'Approved','estimate:accepted',1,true),
	 ($1,$2,'Declined','estimate:rejected',1,true),($1,$2,'Finished','estimate:completed',1,true)`, company, workflow); err != nil {
		t.Fatal(err)
	}
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='estimate:draft'`, workflow), &draftID)
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='estimate:sent'`, workflow), &sentID)
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='estimate:estimate'`, workflow), &otherID)
	mustScan(t, db.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,status_id,title) VALUES($1,$2,$3,'Proposal') RETURNING id`, company, customer, draftID), &estimateID)

	hook := NewAcceptanceHook(New(db))
	d := delivery.Delivery{CompanyID: company, ActorID: admin, DocumentType: "estimate", DocumentID: estimateID, ExpectedStatusID: &draftID}
	withTx(t, db, func(tx pgx.Tx) error { return hook.OnAccepted(ctx, tx, d) })
	var actual int64
	var logs int
	mustScan(t, db.QueryRow(ctx, `SELECT status_id FROM estimates WHERE id=$1`, estimateID), &actual)
	mustScan(t, db.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='estimate' AND object_id=$1 AND action='status_transitioned'`, estimateID), &logs)
	if actual != sentID || logs != 1 {
		t.Fatalf("unchanged delivery status=%d logs=%d, want %d/1", actual, logs, sentID)
	}

	if err := New(db).TransitionEstimate(ctx, Actor{ID: admin, CompanyID: company, Role: "admin"}, estimateID, otherID); err != nil {
		t.Fatal(err)
	}
	withTx(t, db, func(tx pgx.Tx) error { return hook.OnAccepted(ctx, tx, d) })
	mustScan(t, db.QueryRow(ctx, `SELECT status_id FROM estimates WHERE id=$1`, estimateID), &actual)
	mustScan(t, db.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='estimate' AND object_id=$1 AND action='status_transitioned'`, estimateID), &logs)
	if actual != otherID || logs != 2 {
		t.Fatalf("stale delivery status=%d logs=%d, want %d/2", actual, logs, otherID)
	}
}

func withTx(t *testing.T, db *pgxpool.Pool, fn func(pgx.Tx) error) {
	t.Helper()
	tx, err := db.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = fn(tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func statusDB(t *testing.T) (*pgxpool.Pool, int64, int64, int64, int64) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := fmt.Sprintf("freefsm_statusflow_%d", time.Now().UnixNano())
	if _, err = adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminPool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`) })
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dsn, "://") {
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += "?search_path=" + schema
	}
	wrapped, err := database.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wrapped.Close)
	if err = wrapped.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	var company, admin, tech, customer int64
	mustScan(t, wrapped.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Status','status-flow') RETURNING id`), &company)
	mustScan(t, wrapped.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'admin@status.test','x','Admin','admin') RETURNING id`, company), &admin)
	mustScan(t, wrapped.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'tech@status.test','x','Tech','tech') RETURNING id`, company), &tech)
	mustScan(t, wrapped.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, company), &customer)
	return wrapped.Pool, company, admin, tech, customer
}

type scanner interface{ Scan(...any) error }

func mustScan(t *testing.T, row scanner, dest ...any) {
	t.Helper()
	if err := row.Scan(dest...); err != nil {
		t.Fatal(err)
	}
}
