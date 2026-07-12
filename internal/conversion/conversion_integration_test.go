package conversion_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fixture struct {
	db                              *database.DB
	company, customer, admin, tech  int64
	estimateDraft, estimateAccepted int64
	job                             int64
}

func TestConvertRevertAndReconvertIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var estimateID int64
	err := f.db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,notes,tax_rate,line_items,custom_fields) VALUES($1,$2,$3,$4,'Quoted work','source notes','7.5','[{"title":"Work","unit_price":100,"quantity":1}]','[{"definition_id":1,"value":"source"}]') RETURNING id`, f.company, f.customer, f.job, f.estimateAccepted).Scan(&estimateID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by) VALUES($1,'estimate',$2,'a','b','text/plain',1,'p',$3)`, f.company, estimateID, f.admin); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `INSERT INTO comments(company_id,object_type,object_id,author_id,content) VALUES($1,'estimate',$2,$3,'hello')`, f.company, estimateID, f.admin); err != nil {
		t.Fatal(err)
	}

	svc := conversion.New(f.db.Pool)
	actor := conversion.Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	op := conversion.Operation{Key: uuid.New()}
	first, err := svc.Convert(ctx, actor, conversion.ConvertRequest{Operation: op, EstimateID: estimateID})
	if err != nil {
		t.Fatal(err)
	}
	if first.InvoiceID == estimateID {
		t.Fatalf("invoice reused estimate identity %d", estimateID)
	}
	replay, err := svc.Convert(ctx, actor, conversion.ConvertRequest{Operation: op, EstimateID: estimateID})
	if err != nil || replay.CycleID != first.CycleID {
		t.Fatalf("replay=%#v err=%v", replay, err)
	}
	var otherEstimateID int64
	if err = f.db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,line_items) VALUES($1,$2,$3,$4,'Other','[]') RETURNING id`, f.company, f.customer, f.job, f.estimateAccepted).Scan(&otherEstimateID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Convert(ctx, actor, conversion.ConvertRequest{Operation: op, EstimateID: otherEstimateID}); !errors.Is(err, conversion.ErrIdempotencyConflict) {
		t.Fatalf("conflict=%v", err)
	}
	var hidden bool
	var moved int
	_ = f.db.Pool.QueryRow(ctx, `SELECT conversion_hidden_at IS NOT NULL FROM estimates WHERE id=$1`, estimateID).Scan(&hidden)
	_ = f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE object_type='invoice' AND object_id=$1`, first.InvoiceID).Scan(&moved)
	if !hidden || moved != 1 {
		t.Fatalf("hidden=%v moved=%d", hidden, moved)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE invoices SET title='Current invoice',notes='current notes' WHERE id=$1`, first.InvoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `INSERT INTO comments(company_id,object_type,object_id,author_id,content) VALUES($2,'invoice',$1,$3,'invoice comment')`, first.InvoiceID, f.company, f.admin); err != nil {
		t.Fatal(err)
	}
	reverted, err := svc.Revert(ctx, actor, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: first.InvoiceID})
	if err != nil {
		t.Fatal(err)
	}
	if !reverted.Reverted {
		t.Fatal("revert result not marked reverted")
	}
	var title, role string
	if err = f.db.Pool.QueryRow(ctx, `SELECT e.title,split_part(s.category_key,':',2) FROM estimates e JOIN statuses s ON s.id=e.status_id WHERE e.id=$1 AND e.conversion_hidden_at IS NULL`, estimateID).Scan(&title, &role); err != nil {
		t.Fatal(err)
	}
	if title != "Current invoice" || role != "draft" {
		t.Fatalf("restored title=%q role=%q", title, role)
	}
	tx, txErr := f.db.Pool.Begin(ctx)
	if txErr != nil {
		t.Fatal(txErr)
	}
	if _, txErr = tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`); txErr == nil {
		_, txErr = tx.Exec(ctx, `UPDATE estimates SET status_id=$1 WHERE id=$2`, f.estimateAccepted, estimateID)
	}
	if txErr == nil {
		txErr = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if txErr != nil {
		t.Fatal(txErr)
	}
	second, err := svc.Convert(ctx, actor, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: estimateID})
	if err != nil {
		t.Fatal(err)
	}
	if second.InvoiceID == first.InvoiceID || second.InvoiceNumber <= first.InvoiceNumber {
		t.Fatalf("first=%#v second=%#v", first, second)
	}
	timeline, err := svc.Timeline(ctx, actor, estimateID)
	if err != nil {
		t.Fatal(err)
	}
	if len(timeline) < 3 {
		t.Fatalf("combined timeline entries=%d", len(timeline))
	}
}

func TestConversionPolicyStatusArchiveAndSettlementBlockersIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	svc := conversion.New(f.db.Pool)
	create := func(status int64, job any) int64 {
		var id int64
		if err := f.db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,line_items) VALUES($1,$2,$3,$4,'Estimate','[]') RETURNING id`, f.company, f.customer, job, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	admin := conversion.Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	tech := conversion.Actor{ID: f.tech, CompanyID: f.company, Role: "tech"}
	if _, err := svc.Convert(ctx, admin, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: create(f.estimateDraft, f.job)}); err != nil {
		t.Fatalf("active draft conversion=%v", err)
	}
	if _, err := svc.Convert(ctx, tech, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: create(f.estimateAccepted, nil)}); !errors.Is(err, conversion.ErrForbidden) {
		t.Fatalf("customer-only tech conversion=%v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, f.tech); err != nil {
		t.Fatal(err)
	}
	r, err := svc.Convert(ctx, tech, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: create(f.estimateAccepted, f.job)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE invoices SET deleted_at=now(),settlement_state='paid' WHERE id=$1`, r.InvoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Revert(ctx, tech, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: r.InvoiceID}); !errors.Is(err, conversion.ErrArchived) {
		t.Fatalf("archived revert=%v", err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE invoices SET deleted_at=NULL WHERE id=$1`, r.InvoiceID); err != nil {
		t.Fatal(err)
	}
	var payment uuid.UUID = uuid.New()
	if _, err = f.db.Pool.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,invoice_id,amount_cents,method,received_date,actor_id) VALUES($1,$2,$3,$4,1,'cash',CURRENT_DATE,$5)`, payment, f.company, f.customer, r.InvoiceID, f.admin); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `INSERT INTO payment_invoice_allocations VALUES($1,$2,1)`, payment, r.InvoiceID); err != nil {
		t.Fatal(err)
	}
	e, err := svc.RevertEligibility(ctx, tech, r.InvoiceID)
	if err != nil {
		t.Fatal(err)
	}
	if e.Allowed || len(e.Blockers) == 0 {
		t.Fatalf("eligibility=%#v", e)
	}
	if _, err = svc.Revert(ctx, tech, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: r.InvoiceID}); !errors.Is(err, conversion.ErrSettlement) {
		t.Fatalf("settled revert=%v", err)
	}
}

func TestReplayReauthorizesCurrentActorIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	svc := conversion.New(f.db.Pool)
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, f.tech); err != nil {
		t.Fatal(err)
	}
	var estimateID int64
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,line_items) VALUES($1,$2,$3,$4,'Replay','[]') RETURNING id`, f.company, f.customer, f.job, f.estimateAccepted).Scan(&estimateID); err != nil {
		t.Fatal(err)
	}
	actor := conversion.Actor{ID: f.tech, CompanyID: f.company, Role: "tech"}
	op := conversion.Operation{Key: uuid.New()}
	if _, err := svc.Convert(ctx, actor, conversion.ConvertRequest{Operation: op, EstimateID: estimateID}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, f.tech); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Convert(ctx, actor, conversion.ConvertRequest{Operation: op, EstimateID: estimateID}); !errors.Is(err, conversion.ErrForbidden) {
		t.Fatalf("revoked replay=%v", err)
	}
	var otherTech int64
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'other-tech@conversion.test','x','Other','tech') RETURNING id`, f.company).Scan(&otherTech); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Convert(ctx, conversion.Actor{ID: otherTech, CompanyID: f.company, Role: "tech"}, conversion.ConvertRequest{Operation: op, EstimateID: estimateID}); !errors.Is(err, conversion.ErrForbidden) {
		t.Fatalf("cross-user replay=%v", err)
	}
}

func TestCustomFieldKeysMapAndMergeIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	svc := conversion.New(f.db.Pool)
	var estimateShared, estimateOnly, invoiceShared, invoiceOnly int64
	insert := func(object, name, key string) *int64 {
		var id int64
		var err error
		if key == "" {
			err = f.db.Pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type) VALUES($1,$2,$3,'text') RETURNING id`, f.company, object, name).Scan(&id)
		} else {
			err = f.db.Pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,conversion_key) VALUES($1,$2,$3,'text',$4) RETURNING id`, f.company, object, name, key).Scan(&id)
		}
		if err != nil {
			t.Fatal(err)
		}
		return &id
	}
	estimateShared = *insert("estimate", "Old estimate label", "shared")
	estimateOnly = *insert("estimate", "Estimate only", "")
	invoiceShared = *insert("invoice", "Renamed invoice label", "shared")
	invoiceOnly = *insert("invoice", "Invoice only", "")
	custom := fmt.Sprintf(`[{"definition_id":%d,"value":{"typed":true}},{"definition_id":%d,"value":"estimate-only"}]`, estimateShared, estimateOnly)
	var estimateID int64
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,line_items,custom_fields) VALUES($1,$2,$3,$4,'Fields','[]',$5) RETURNING id`, f.company, f.customer, f.job, f.estimateAccepted, custom).Scan(&estimateID); err != nil {
		t.Fatal(err)
	}
	actor := conversion.Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	r, err := svc.Convert(ctx, actor, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: estimateID})
	if err != nil {
		t.Fatal(err)
	}
	var invoiceCustom string
	if err = f.db.Pool.QueryRow(ctx, `SELECT custom_fields::text FROM invoices WHERE id=$1`, r.InvoiceID).Scan(&invoiceCustom); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(invoiceCustom, fmt.Sprintf(`"definition_id": %d`, invoiceShared)) || strings.Contains(invoiceCustom, "estimate-only") {
		t.Fatalf("mapped invoice custom fields=%s", invoiceCustom)
	}
	current := fmt.Sprintf(`[{"definition_id":%d,"value":{"typed":"current"}},{"definition_id":%d,"value":"invoice-only"}]`, invoiceShared, invoiceOnly)
	if _, err = f.db.Pool.Exec(ctx, `UPDATE invoices SET custom_fields=$1 WHERE id=$2`, current, r.InvoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Revert(ctx, actor, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: r.InvoiceID}); err != nil {
		t.Fatal(err)
	}
	var restored string
	if err = f.db.Pool.QueryRow(ctx, `SELECT custom_fields::text FROM estimates WHERE id=$1`, estimateID).Scan(&restored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(restored, "estimate-only") || !strings.Contains(restored, "current") || strings.Contains(restored, "invoice-only") {
		t.Fatalf("restored custom fields=%s", restored)
	}
}

func TestCanonicalMoneyAndTombstoneGuardsIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	svc := conversion.New(f.db.Pool)
	actor := conversion.Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	convert := func(items, tax string) (conversion.Result, error) {
		var id int64
		if err := f.db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,line_items,tax_rate) VALUES($1,$2,$3,$4,'Money',$5,$6) RETURNING id`, f.company, f.customer, f.job, f.estimateAccepted, items, tax).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return svc.Convert(ctx, actor, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: id})
	}
	if _, err := convert(`"["`, "0"); err == nil {
		t.Fatal("malformed line items converted")
	}
	if _, err := convert(`[{"title":"Work","quantity":1,"unit_price":-1}]`, "0"); err == nil {
		t.Fatal("negative line item converted")
	}
	if _, err := convert(`[{"title":"Work","quantity":1}]`, "101"); err == nil {
		t.Fatal("invalid tax converted")
	}
	r, err := convert("[]", "0")
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err = f.db.Pool.QueryRow(ctx, `SELECT settlement_state FROM invoices WHERE id=$1`, r.InvoiceID).Scan(&state); err != nil || state != "paid" {
		t.Fatalf("zero state=%q err=%v", state, err)
	}
	tx, err := f.db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.conversion','on',false)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE estimates SET conversion_hidden_at=NULL WHERE id=$1`, r.EstimateID); err == nil {
		t.Fatal("direct GUC bypass restored estimate")
	}
	_ = tx.Rollback(ctx)
	if _, err = svc.Revert(ctx, actor, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: r.InvoiceID}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,invoice_id,amount_cents,method,received_date,actor_id) VALUES($1,$2,$3,$4,1,'cash',CURRENT_DATE,$5)`, uuid.New(), f.company, f.customer, r.InvoiceID, f.admin); err == nil {
		t.Fatal("direct settlement insert accepted hidden invoice")
	}
	settlements := settlement.New(f.db.Pool)
	settlementActor := settlement.Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	if _, err = settlements.RecordPayment(ctx, settlementActor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: uuid.NewString()}, InvoiceID: r.InvoiceID, AmountCents: 1, Method: settlement.Cash, ReceivedDate: settlement.Date(time.Now().Format("2006-01-02"))}); !errors.Is(err, settlement.ErrHidden) {
		t.Fatalf("hidden payment error=%v", err)
	}
	if _, err = settlements.InvoiceSettlement(ctx, settlementActor, r.InvoiceID); err == nil {
		t.Fatal("hidden invoice exposed by settlement query")
	}
}

func TestConcurrentConversionsAllocateSequentialInvoiceNumbersIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	svc := conversion.New(f.db.Pool)
	actor := conversion.Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	const count = 8
	ids := make([]int64, count)
	for i := range ids {
		if err := f.db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,line_items) VALUES($1,$2,$3,$4,$5,'[]') RETURNING id`, f.company, f.customer, f.job, f.estimateAccepted, fmt.Sprintf("Concurrent %d", i)).Scan(&ids[i]); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make([]conversion.Result, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = svc.Convert(ctx, actor, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: ids[i]})
		}(i)
	}
	close(start)
	wg.Wait()
	numbers := make(map[int64]bool, count)
	for i, err := range errs {
		if err != nil {
			t.Fatalf("conversion %d: %v", i, err)
		}
		numbers[results[i].InvoiceNumber] = true
	}
	for number := int64(10); number < 10+count; number++ {
		if !numbers[number] {
			t.Fatalf("missing invoice number %d: %#v", number, numbers)
		}
	}
	var cycles, invoices int
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM estimate_invoice_conversion_cycles WHERE company_id=$1`, f.company).Scan(&cycles); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM invoices WHERE company_id=$1`, f.company).Scan(&invoices); err != nil {
		t.Fatal(err)
	}
	if cycles != count || invoices != count {
		t.Fatalf("cycles=%d invoices=%d", cycles, invoices)
	}
}

func TestDatabaseRejectsInvalidDocumentStatusOwnershipIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var invoiceStatus int64
	if err := f.db.Pool.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE s.company_id=$1 AND w.object_type='invoice'`, f.company).Scan(&invoiceStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO estimates(company_id,customer_id,status_id,title) VALUES($1,$2,$3,'wrong workflow')`, f.company, f.customer, invoiceStatus); err == nil {
		t.Fatal("database accepted invoice status on estimate")
	}
	var otherCompany, otherWorkflow, otherStatus int64
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other','other-status') RETURNING id`).Scan(&otherCompany); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Other Estimate','estimate') RETURNING id`, otherCompany).Scan(&otherWorkflow); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
	 ($1,$2,'Draft','estimate:draft',1,true),($1,$2,'Custom','estimate:estimate',1,true),($1,$2,'Sent','estimate:sent',1,true),
	 ($1,$2,'Accepted','estimate:accepted',1,true),($1,$2,'Rejected','estimate:rejected',1,true),($1,$2,'Completed','estimate:completed',1,true)`, otherCompany, otherWorkflow); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='estimate:estimate'`, otherWorkflow).Scan(&otherStatus); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO estimates(company_id,customer_id,status_id,title) VALUES($1,$2,$3,'cross company')`, f.company, f.customer, otherStatus); err == nil {
		t.Fatal("database accepted cross-company estimate status")
	}
	var custom int64
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) SELECT $1,id,'Custom','estimate:estimate',2,false FROM status_workflows WHERE company_id=$1 AND object_type='estimate' RETURNING id`, f.company).Scan(&custom); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO estimates(company_id,customer_id,status_id,title) VALUES($1,$2,$3,'valid custom')`, f.company, f.customer, custom); err != nil {
		t.Fatalf("valid custom status: %v", err)
	}
}

func TestConcurrentIncompatibleCustomFieldPairCannotCommitIntegration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, objectType := range []string{"estimate", "invoice"} {
		wg.Add(1)
		go func(i int, objectType string) {
			defer wg.Done()
			<-start
			fieldType := "text"
			if objectType == "invoice" {
				fieldType = "number"
			}
			_, errs[i] = f.db.Pool.Exec(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,options,conversion_key) VALUES($1,$2,$3,$4,'[]','racing-pair')`, f.company, objectType, objectType, fieldType)
		}(i, objectType)
	}
	close(start)
	wg.Wait()
	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		}
	}
	if succeeded > 1 {
		t.Fatalf("both incompatible definitions committed: %v", errs)
	}
	var inconsistent bool
	if err := f.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM custom_field_definitions e JOIN custom_field_definitions i ON i.company_id=e.company_id AND i.conversion_key=e.conversion_key AND i.object_type='invoice' WHERE e.company_id=$1 AND e.object_type='estimate' AND e.field_type<>i.field_type)`, f.company).Scan(&inconsistent); err != nil {
		t.Fatal(err)
	}
	if inconsistent {
		t.Fatal("incompatible conversion pair remains committed")
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL conversion tests")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := fmt.Sprintf("freefsm_conversion_%d", time.Now().UnixNano())
	if _, err = adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminPool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`) })
	db, err := database.Connect(ctx, withSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err = db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	f := &fixture{db: db}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Conversion','conversion-test') RETURNING id`).Scan(&f.company); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO company_settings(company_id,business_name,timezone,next_invoice_number) VALUES($1,'Conversion','UTC',10)`, f.company); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, f.company).Scan(&f.customer); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'admin@conversion.test','x','Admin','admin') RETURNING id`, f.company).Scan(&f.admin); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'tech@conversion.test','x','Tech','tech') RETURNING id`, f.company).Scan(&f.tech); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Work') RETURNING id`, f.company, f.customer).Scan(&f.job); err != nil {
		t.Fatal(err)
	}
	var ew, iw int64
	_ = db.Pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Estimate','estimate') RETURNING id`, f.company).Scan(&ew)
	_ = db.Pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoice','invoice') RETURNING id`, f.company).Scan(&iw)
	_, _ = db.Pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
	 ($1,$2,'Draft','estimate:draft',1,true),($1,$2,'Approved','estimate:accepted',1,true),
	 ($1,$2,'Estimate','estimate:estimate',1,true),($1,$2,'Sent','estimate:sent',1,true),
	 ($1,$2,'Rejected','estimate:rejected',1,true),($1,$2,'Completed','estimate:completed',1,true)`, f.company, ew)
	_ = db.Pool.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='estimate:draft'`, ew).Scan(&f.estimateDraft)
	_ = db.Pool.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='estimate:accepted'`, ew).Scan(&f.estimateAccepted)
	_, err = db.Pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
	 ($1,$2,'Draft','invoice:draft',1,true),($1,$2,'Invoiced','invoice:invoiced',1,true),($1,$2,'Sent','invoice:sent',1,true),
	 ($1,$2,'Partially Paid','invoice:partially_paid',1,true),($1,$2,'Paid','invoice:paid',1,true),($1,$2,'Void','invoice:void',1,true)`, f.company, iw)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + "?search_path=" + schema
}
