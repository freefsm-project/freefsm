package authorization_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type commercialReportFixture struct {
	pool                                                            *pgxpool.Pool
	svc                                                             *authorization.Service
	actor                                                           authorization.Actor
	company, customer, job, otherJob, estimateStatus, invoiceStatus int64
}

func TestDocumentListInvoiceEffectiveStatusAndUIFacets(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	var id, paidStatus int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,status_id,title,invoice_date,due_date,line_items,settlement_state) VALUES($1,$2,$3,$4,'Zero invoice','2024-03-10','0001-01-01','[]','paid') RETURNING id`, f.company, f.customer, f.job, f.invoiceStatus).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(f.pool).FinalizeInvoice(ctx, statusflow.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}, id); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT status_id FROM invoice_effective_status WHERE invoice_id=$1`, id).Scan(&paidStatus); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.DocumentList(ctx, f.actor, "invoice", authorization.DocumentListFilter{StatusID: paidStatus}, authorization.Assigned)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 1 || r.Rows[0].Category != "invoice:paid" || r.Rows[0].SettlementState != "paid" || r.Rows[0].DueDate != "" {
		t.Fatalf("effective UI projection: %+v", r)
	}
	if err := f.svc.WithinActor(ctx, f.actor, func(tx pgx.Tx, p authorization.Principal) error {
		r, err := authorization.DocumentListSnapshot(ctx, tx, p, "invoice", authorization.DocumentListFilter{StatusID: paidStatus}, authorization.Assigned, true)
		if err != nil {
			return err
		}
		if r.Total != 1 || r.Rows[0].SettlementState != "" || len(r.Rows[0].Financial) != 0 {
			t.Fatalf("CSV financial leak: %+v", r)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	role, err := f.svc.SaveRole(ctx, f.actor, 0, "Operational Invoice exporter", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.export", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	var user int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,'invoice-report@test','Exporter','hash','tech',$2) RETURNING id`, f.company, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, user); err != nil {
		t.Fatal(err)
	}
	a := authorization.Actor{UserID: user}
	r, err = f.svc.DocumentList(ctx, a, "invoice", authorization.DocumentListFilter{}, authorization.Assigned)
	if err != nil || r.Total != 1 || r.Rows[0].SettlementState != "" || r.Rows[0].Customer != "" || len(r.Rows[0].Financial) != 0 {
		t.Fatalf("UI facet denial: %+v %v", r, err)
	}
	err = f.svc.WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		_, err := authorization.DocumentListSnapshot(ctx, tx, p, "invoice", authorization.DocumentListFilter{}, authorization.All, true)
		return err
	})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("J Export became A through A View: %v", err)
	}
	ref := r.Rows[0].Reference
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET deleted_at=now() WHERE id=$1`, f.job); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.DocumentList(ctx, a, "invoice", authorization.DocumentListFilter{}, authorization.Assigned)
	if err != nil || r.Total != 0 {
		t.Fatalf("archived Assigned anchor: %+v %v", r, err)
	}
	err = f.svc.WithinActor(ctx, a, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckDocumentListReference(ctx, tx, p, "invoice", ref, authorization.Assigned, true)
	})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("archived export reference: %v", err)
	}
}

func newCommercialReportFixture(t *testing.T) commercialReportFixture {
	t.Helper()
	pool, svc := fixture(t)
	f := commercialReportFixture{pool: pool, svc: svc}
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) SELECT id,'commercial-report@test','Report Admin','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin' FROM companies LIMIT 1 RETURNING id,company_id`).Scan(&f.actor.UserID, &f.company); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Report customer') RETURNING id`, f.company).Scan(&f.customer); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []*int64{&f.job, &f.otherJob} {
		if err := pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Report work') RETURNING id`, f.company, f.customer).Scan(dest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, f.actor.UserID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.company_id=$1 AND s.category_key='estimate:accepted'`, f.company).Scan(&f.estimateStatus); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.company_id=$1 AND s.category_key='invoice:draft'`, f.company).Scan(&f.invoiceStatus); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f commercialReportFixture) estimate(t *testing.T, job int64, title string, created time.Time) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title,created_at,line_items) VALUES($1,$2,nullif($3,0),$4,$5,$6,'[]') RETURNING id`, f.company, f.customer, job, f.estimateStatus, title, created).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDocumentListNormalizers(t *testing.T) {
	for _, f := range []authorization.DocumentListFilter{{StatusID: -1}, {CustomerID: -1}, {Valid: "expired"}, {Sort: "created_at; DROP TABLE estimates"}} {
		if _, err := authorization.NormalizeDocumentListFilter("estimate", f); err == nil {
			t.Fatalf("accepted %+v", f)
		}
	}
	if _, err := authorization.NormalizeDocumentListFilter("invoice", authorization.DocumentListFilter{Valid: "current"}); err == nil {
		t.Fatal("Invoice accepted Estimate validity")
	}
	f, err := authorization.NormalizeDocumentListFilter("estimate", authorization.DocumentListFilter{Search: "  literal %  "})
	if err != nil || f.Search != "literal %" || f.Sort != "created_desc" {
		t.Fatalf("normalized %+v: %v", f, err)
	}
}

func TestDocumentListReferenceMatchesOperationalCustomerAnchors(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	id := f.estimate(t, f.job, "Customer anchor", time.Now())
	var otherCustomer int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Changed Customer') RETURNING id`, f.company).Scan(&otherCustomer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.otherJob, f.actor.UserID); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.DocumentList(ctx, f.actor, "estimate", authorization.DocumentListFilter{}, authorization.Assigned)
	if err != nil || r.Total != 1 {
		t.Fatalf("initial snapshot: %+v %v", r, err)
	}
	ref := r.Rows[0].Reference
	check := func(scope authorization.Scope) error {
		return f.svc.WithinActor(ctx, f.actor, func(tx pgx.Tx, p authorization.Principal) error {
			return authorization.CheckDocumentListReference(ctx, tx, p, "estimate", ref, scope, true)
		})
	}
	if _, err := f.pool.Exec(ctx, `UPDATE estimates SET job_id=$1 WHERE id=$2`, f.otherJob, id); err != nil {
		t.Fatal(err)
	}
	// Normal writes cascade operational Customer changes. Inject a stale
	// current link in this private schema to prove selection and recheck both
	// fail closed if retained data violates that invariant.
	if _, err := f.pool.Exec(ctx, `ALTER TABLE jobs DISABLE TRIGGER reassign_job_commercial_anchors`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET customer_id=$1 WHERE id=$2`, otherCustomer, f.otherJob); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE jobs ENABLE TRIGGER reassign_job_commercial_anchors`); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.DocumentList(ctx, f.actor, "estimate", authorization.DocumentListFilter{}, authorization.Assigned)
	if err != nil || r.Total != 0 {
		t.Fatalf("mismatched current anchor selected: %+v %v", r, err)
	}
	if err := check(authorization.Assigned); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("mismatched current anchor rechecked: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET customer_id=$1 WHERE id=$2`, f.customer, f.otherJob); err != nil {
		t.Fatal(err)
	}
	if err := check(authorization.Assigned); err != nil {
		t.Fatalf("valid moved anchor: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET customer_id=$1 WHERE id=$2`, otherCustomer, f.job); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.DocumentList(ctx, f.actor, "estimate", authorization.DocumentListFilter{}, authorization.Assigned)
	if err != nil || r.Total != 1 {
		t.Fatalf("valid current selection: %+v %v", r, err)
	}
	if err := check(authorization.Assigned); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("mismatched original anchor rechecked: %v", err)
	}
	if err := check(authorization.All); err != nil {
		t.Fatalf("All-scope authority acquired Assigned constraint: %v", err)
	}
}

func TestDocumentListArchivedCustomerProjectionRecheckAndContextAnchors(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	var invoice int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,status_id,title,invoice_date,line_items,settlement_state) VALUES($1,$2,$3,$4,'Retained paid invoice','2024-03-10','[]','paid') RETURNING id`, f.company, f.customer, f.job, f.invoiceStatus).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(f.pool).FinalizeInvoice(ctx, statusflow.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}, invoice); err != nil {
		t.Fatal(err)
	}
	makeUser := func(name string, customerScope authorization.Scope) authorization.Actor {
		t.Helper()
		role, err := f.svc.SaveRole(ctx, f.actor, 0, name, []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.export", Scope: authorization.All}, {Permission: "customer.view", Scope: customerScope}, {Permission: "job.view", Scope: authorization.Assigned}})
		if err != nil {
			t.Fatal(err)
		}
		var user int64
		if err := f.pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,$2,$2,'hash','tech',$3) RETURNING id`, f.company, name+"@test", role).Scan(&user); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$3),($2,$3)`, f.job, f.otherJob, user); err != nil {
			t.Fatal(err)
		}
		return authorization.Actor{UserID: user}
	}
	contextActor := makeUser("Context report", authorization.Context)
	r, err := f.svc.DocumentList(ctx, contextActor, "invoice", authorization.DocumentListFilter{}, authorization.All)
	if err != nil || r.Total != 1 || r.Rows[0].Customer == "" {
		t.Fatalf("Context snapshot: %+v %v", r, err)
	}
	ref := r.Rows[0].Reference
	if _, err := f.pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2`, f.otherJob, invoice); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, contextActor.UserID); err != nil {
		t.Fatal(err)
	}
	err = f.svc.WithinActor(ctx, contextActor, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckDocumentListReference(ctx, tx, p, "invoice", ref, authorization.All, true)
	})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("sibling Job revived original Customer context: %v", err)
	}
	reader := makeUser("Active Customer report", authorization.All)
	if _, err := f.pool.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, f.customer); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.WithinActor(ctx, f.actor, func(tx pgx.Tx, p authorization.Principal) error {
		r, err := authorization.DocumentListSnapshot(ctx, tx, p, "invoice", authorization.DocumentListFilter{CustomerID: f.customer}, authorization.All, true)
		if err != nil {
			return err
		}
		if r.Total != 1 || r.Rows[0].Customer == "" {
			t.Fatalf("authorized archived name: %+v", r)
		}
		return authorization.CheckDocumentListReference(ctx, tx, p, "invoice", r.Rows[0].Reference, authorization.All, true)
	}); err != nil {
		t.Fatalf("immediately blocked authorized artifact: %v", err)
	}
	r, err = f.svc.DocumentList(ctx, reader, "invoice", authorization.DocumentListFilter{}, authorization.All)
	if err != nil || r.Total != 1 || r.Rows[0].Customer != "" || r.Rows[0].Reference.CustomerVisible {
		t.Fatalf("archived name leaked: %+v %v", r, err)
	}
	err = f.svc.WithinActor(ctx, reader, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckDocumentListReference(ctx, tx, p, "invoice", r.Rows[0].Reference, authorization.All, true)
	})
	if err != nil {
		t.Fatalf("name-free artifact blocked: %v", err)
	}
	if _, err := f.svc.DocumentList(ctx, reader, "invoice", authorization.DocumentListFilter{CustomerID: f.customer, Search: "empty"}, authorization.All); !errors.Is(err, authorization.ErrNotFound) {
		t.Fatalf("archived selected filter: %v", err)
	}
}

func TestDocumentListSnapshotScopeFiltersAndReferences(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	stamp := time.Date(2024, 3, 10, 7, 0, 0, 0, time.UTC)
	first := f.estimate(t, f.job, "literal %", stamp)
	second := f.estimate(t, f.job, "literal %", stamp)
	f.estimate(t, f.otherJob, "literal %", stamp)
	f.estimate(t, 0, "literal %", stamp)
	for range 28 {
		f.estimate(t, f.otherJob, "Full matching collection", stamp)
	}
	full, err := f.svc.DocumentList(ctx, f.actor, "estimate", authorization.DocumentListFilter{Search: "Full matching collection"}, authorization.All)
	if err != nil || full.Total != 28 || len(full.Rows) != 28 {
		t.Fatalf("full selection was paginated: %+v %v", full, err)
	}
	r, err := f.svc.DocumentList(ctx, f.actor, "estimate", authorization.DocumentListFilter{Search: "%"}, authorization.Assigned)
	if err != nil {
		t.Fatal(err)
	}
	if r.Total != 2 || r.Rows[0].ID != second || r.Rows[1].ID != first {
		t.Fatalf("scope/order: %+v", r)
	}
	if err := f.svc.WithinSnapshotPermission(ctx, f.actor, "estimate.export", authorization.Assigned, func(tx pgx.Tx, p authorization.Principal) error {
		x, err := authorization.DocumentListSnapshot(ctx, tx, p, "estimate", authorization.DocumentListFilter{}, authorization.Assigned, true)
		if err != nil {
			return err
		}
		var at time.Time
		if err := tx.QueryRow(ctx, `SELECT transaction_timestamp()`).Scan(&at); err != nil {
			return err
		}
		if !x.AsOf.Equal(at) || len(x.Rows) != 2 {
			t.Fatalf("snapshot: %+v", x)
		}
		for _, row := range x.Rows {
			if len(row.Financial) != 0 || row.SettlementState != "" {
				t.Fatal("operational export leaked financial facet")
			}
			if err := authorization.CheckDocumentListReference(ctx, tx, p, "estimate", row.Reference, authorization.Assigned, true); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE estimates SET valid_through='2000-01-01' WHERE id=$1`, first); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.DocumentList(ctx, f.actor, "estimate", authorization.DocumentListFilter{Valid: "current"}, authorization.Assigned)
	if err != nil || r.Total != 1 {
		t.Fatalf("valid=current: %+v %v", r, err)
	}
	if _, err = f.svc.DocumentList(ctx, f.actor, "estimate", authorization.DocumentListFilter{Search: "no matches", CustomerID: 999999}, authorization.All); !errors.Is(err, authorization.ErrNotFound) {
		t.Fatalf("unauthorized empty selection: %v", err)
	}
	if _, err = f.svc.DocumentList(ctx, f.actor, "estimate", authorization.DocumentListFilter{Search: "no matches", StatusID: f.invoiceStatus}, authorization.All); !errors.Is(err, authorization.ErrNotFound) {
		t.Fatalf("wrong-type empty status selection: %v", err)
	}
	ref := r.Rows[0].Reference
	if _, err := f.pool.Exec(ctx, `UPDATE estimates SET job_id=$1 WHERE id=$2`, f.otherJob, second); err != nil {
		t.Fatal(err)
	}
	err = f.svc.WithinActor(ctx, f.actor, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckDocumentListReference(ctx, tx, p, "estimate", ref, authorization.Assigned, true)
	})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("current anchor loss: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.otherJob, f.actor.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, f.job); err != nil {
		t.Fatal(err)
	}
	err = f.svc.WithinActor(ctx, f.actor, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckDocumentListReference(ctx, tx, p, "estimate", ref, authorization.Assigned, true)
	})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("original anchor loss: %v", err)
	}
}
