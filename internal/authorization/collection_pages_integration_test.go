package authorization_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type collectionPageTrace struct {
	mu      sync.Mutex
	queries []pgx.TraceQueryStartData
}

func (t *collectionPageTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(d.SQL, "/* collection-page:") {
		t.mu.Lock()
		t.queries = append(t.queries, d)
		t.mu.Unlock()
	}
	return ctx
}
func (*collectionPageTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type collectionPlan struct {
	NodeType   string           `json:"Node Type"`
	ActualRows float64          `json:"Actual Rows"`
	Plans      []collectionPlan `json:"Plans"`
}

func (p collectionPlan) hasPageLimit() bool {
	if p.NodeType == "Limit" && p.ActualRows == 25 {
		return true
	}
	for _, child := range p.Plans {
		if child.hasPageLimit() {
			return true
		}
	}
	return false
}

func TestCollectionPagesBoundSQLAndPreserveDomainRows(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	for _, sql := range []string{
		`INSERT INTO jobs(company_id,customer_id,job_type) SELECT $1,$2,'page-match '||lpad(n::text,3,'0') FROM generate_series(1,260) n`,
		`INSERT INTO estimates(company_id,customer_id,title) SELECT $1,$2,'page-match '||lpad(n::text,3,'0') FROM generate_series(1,260) n`,
		`INSERT INTO invoices(company_id,customer_id,title) SELECT $1,$2,'page-match '||lpad(n::text,3,'0') FROM generate_series(1,260) n`,
		`INSERT INTO customer_contacts(company_id,customer_id,first_name,last_name) SELECT $1,$2,'page-match',lpad(n::text,3,'0') FROM generate_series(1,260) n`,
		`INSERT INTO locations(company_id,object_type,object_id,title) SELECT $1,'customer',$2,'page-match '||lpad(n::text,3,'0') FROM generate_series(1,260) n`,
	} {
		if _, err := f.pool.Exec(ctx, sql, f.company, f.customer); err != nil {
			t.Fatal(err)
		}
	}
	trace := &collectionPageTrace{}
	config := f.pool.Config()
	config.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	svc := authorization.New(pool)
	jobFilter := authorization.JobOperationalFilter{Search: "page-match"}
	fullJobs, err := f.svc.JobOperational(ctx, f.actor, jobFilter, authorization.All)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := svc.JobOperationalPage(ctx, f.actor, jobFilter, authorization.All, 2, 25)
	if err != nil || jobs.Total != 260 || !reflect.DeepEqual(jobs.Rows, fullJobs.Rows[25:50]) || len(jobs.UIBillingTypes) != 25 {
		t.Fatalf("Job domain page: total=%d rows=%d err=%v", jobs.Total, len(jobs.Rows), err)
	}
	for _, kind := range []string{"estimate", "invoice"} {
		filter := authorization.DocumentListFilter{Search: "page-match", Sort: "title_asc"}
		full, err := f.svc.DocumentList(ctx, f.actor, kind, filter, authorization.All)
		if err != nil {
			t.Fatal(err)
		}
		page, err := svc.DocumentListPage(ctx, f.actor, kind, filter, authorization.All, 2, 25)
		if err != nil || page.Total != 260 || !reflect.DeepEqual(page.Rows, full.Rows[25:50]) {
			t.Fatalf("%s projection page mismatch: rows=%d total=%d err=%v", kind, len(page.Rows), page.Total, err)
		}
	}
	for _, kind := range []string{"contact", "location"} {
		page, err := svc.CustomerChildrenPage(ctx, f.actor, kind, authorization.CustomerChildPageFilter{Search: "page-match", Page: 2, PerPage: 25})
		if err != nil || page.Total != 260 || len(page.Rows) != 25 || page.Rows[0].Name() != "page-match 026" || page.Rows[24].Name() != "page-match 050" {
			t.Fatalf("%s page: %+v %v", kind, page, err)
		}
	}
	// Explain the exact five production queries captured by pgx, not a
	// reconstructed test query. A real SQL Limit bounds each page/enrichment.
	trace.mu.Lock()
	queries := append([]pgx.TraceQueryStartData(nil), trace.queries...)
	trace.mu.Unlock()
	if len(queries) != 5 {
		t.Fatalf("expected all five bounded SQL selectors, got %d", len(queries))
	}
	for _, query := range queries {
		var raw []byte
		if err := f.pool.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+query.SQL, query.Args...).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var plans []struct{ Plan collectionPlan }
		if err := json.Unmarshal(raw, &plans); err != nil {
			t.Fatal(err)
		}
		if len(plans) != 1 || plans[0].Plan.ActualRows != 25 || !plans[0].Plan.hasPageLimit() {
			t.Fatalf("production query not SQL-bounded: %s", raw)
		}
		t.Logf("bounded production plan: %s", raw)
	}
	for _, size := range []int{25, 50, 100} {
		jobs, err := svc.JobOperationalPage(ctx, f.actor, jobFilter, authorization.All, 1, size)
		if err != nil || len(jobs.Rows) != size || jobs.Total != 260 {
			t.Fatalf("Job size %d: %+v %v", size, jobs, err)
		}
		for _, kind := range []string{"estimate", "invoice"} {
			p, err := svc.DocumentListPage(ctx, f.actor, kind, authorization.DocumentListFilter{Search: "page-match"}, authorization.All, 1, size)
			if err != nil || len(p.Rows) != size || p.Total != 260 {
				t.Fatalf("%s size %d: %+v %v", kind, size, p, err)
			}
		}
		for _, kind := range []string{"contact", "location"} {
			p, err := svc.CustomerChildrenPage(ctx, f.actor, kind, authorization.CustomerChildPageFilter{Search: "page-match", Sort: "name_desc", PerPage: size})
			if err != nil || len(p.Rows) != size || p.Total != 260 || p.Rows[0].Name() != "page-match 260" {
				t.Fatalf("%s descending size %d: %+v %v", kind, size, p, err)
			}
		}
	}
	jobs, err = svc.JobOperationalPage(ctx, f.actor, jobFilter, authorization.All, 100, 25)
	if err != nil || jobs.Total != 260 || len(jobs.Rows) != 0 {
		t.Fatalf("Job empty page loses count: %+v %v", jobs, err)
	}
	for _, kind := range []string{"estimate", "invoice"} {
		p, err := svc.DocumentListPage(ctx, f.actor, kind, authorization.DocumentListFilter{Search: "page-match"}, authorization.All, 100, 25)
		if err != nil || p.Total != 260 || len(p.Rows) != 0 {
			t.Fatalf("%s empty page loses count: %+v %v", kind, p, err)
		}
	}
	for _, kind := range []string{"contact", "location"} {
		p, err := svc.CustomerChildrenPage(ctx, f.actor, kind, authorization.CustomerChildPageFilter{Search: "page-match", Page: 100})
		if err != nil || p.Total != 260 || len(p.Rows) != 0 {
			t.Fatalf("%s empty page loses count: %+v %v", kind, p, err)
		}
	}
	// The explicitly selected Assigned scope must apply before LIMIT even for
	// this broad reader, with hundreds of otherwise matching unassigned rows.
	jobs, err = svc.JobOperationalPage(ctx, f.actor, authorization.JobOperationalFilter{}, authorization.Assigned, 1, 25)
	if err != nil || jobs.Total != 1 || jobs.Rows[0].ID != f.job {
		t.Fatalf("Assigned predicate after page bound: %+v %v", jobs, err)
	}
	for _, kind := range []string{"estimate", "invoice"} {
		var id int64
		if err := f.pool.QueryRow(ctx, `INSERT INTO `+kind+`s(company_id,customer_id,job_id,title) VALUES($1,$2,$3,'page-match assigned') RETURNING id`, f.company, f.customer, f.job).Scan(&id); err != nil {
			t.Fatal(err)
		}
		p, err := svc.DocumentListPage(ctx, f.actor, kind, authorization.DocumentListFilter{Search: "page-match", Sort: "number_asc"}, authorization.Assigned, 1, 25)
		if err != nil || p.Total != 1 || p.Rows[0].ID != id {
			t.Fatalf("%s Assigned predicate after page bound: %+v %v", kind, p, err)
		}
	}
}

func TestCollectionPagesCurrentAuthorityAndFacetBoundaries(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	reader := discoveryActor(t, f,
		authorization.Grant{Permission: "job.view", Scope: authorization.All},
		authorization.Grant{Permission: "estimate.view", Scope: authorization.All},
		authorization.Grant{Permission: "invoice.view", Scope: authorization.All},
		authorization.Grant{Permission: "pricing.view", Scope: authorization.Assigned},
		authorization.Grant{Permission: "contact.view", Scope: authorization.All},
		authorization.Grant{Permission: "location.view", Scope: authorization.All})
	if _, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, reader.UserID); err != nil {
		t.Fatal(err)
	}
	financial, err := commercial.Accept(commercial.Input{TaxRate: "0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range []int64{f.job, f.otherJob} {
		for _, kind := range []string{"estimate", "invoice"} {
			if _, err := f.pool.Exec(ctx, `INSERT INTO `+kind+`s(company_id,customer_id,job_id,title,financial) VALUES($1,$2,$3,'Visible document',$4)`, f.company, f.customer, job, financial); err != nil {
				t.Fatal(err)
			}
		}
	}
	jobs, err := f.svc.JobOperationalPage(ctx, reader, authorization.JobOperationalFilter{}, authorization.All, 1, 25)
	if err != nil || len(jobs.UIBillingTypes) != 1 || jobs.UIBillingTypes[f.job] == "" {
		t.Fatalf("row-specific Job pricing: %+v %v", jobs, err)
	}
	for _, row := range jobs.Rows {
		if row.Customer != "" || row.CustomerID != 0 || row.Reference.CustomerID != 0 {
			t.Fatalf("Job leaked Customer reference: %+v", row)
		}
	}
	for _, kind := range []string{"estimate", "invoice"} {
		page, err := f.svc.DocumentListPage(ctx, reader, kind, authorization.DocumentListFilter{}, authorization.All, 1, 25)
		if err != nil || page.Total != 2 {
			t.Fatalf("%s page: %+v %v", kind, page, err)
		}
		for _, row := range page.Rows {
			if row.Customer != "" || row.Reference.CustomerVisible || row.SettlementState != "" || (len(row.Financial) != 0) != (row.JobID == f.job) {
				t.Fatalf("document facet boundary: %+v", row)
			}
		}
	}
	for _, kind := range []string{"contact", "location"} {
		sql := `INSERT INTO customer_contacts(company_id,customer_id,first_name) VALUES($1,$2,'Éclair')`
		if kind == "location" {
			sql = `INSERT INTO locations(company_id,object_type,object_id,title) VALUES($1,'customer',$2,'Éclair')`
		}
		if _, err := f.pool.Exec(ctx, sql, f.company, f.customer); err != nil {
			t.Fatal(err)
		}
		p, err := f.svc.CustomerChildrenPage(ctx, reader, kind, authorization.CustomerChildPageFilter{Search: "éclair"})
		if err != nil || p.Total != 1 || p.Rows[0].CustomerID != 0 || !p.Rows[0].CanMutate {
			t.Fatalf("child Unicode/reference projection: %+v %v", p, err)
		}
		if _, err := f.svc.CustomerChildrenPage(ctx, reader, kind, authorization.CustomerChildPageFilter{Archived: true}); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("child archive grant missing: %v", err)
		}
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1`, reader.UserID); err != nil {
		t.Fatal(err)
	}
	jobs, err = f.svc.JobOperationalPage(ctx, reader, authorization.JobOperationalFilter{}, authorization.All, 1, 25)
	if err != nil || len(jobs.UIBillingTypes) != 0 {
		t.Fatalf("stale Job pricing scope: %+v %v", jobs, err)
	}
	for _, kind := range []string{"estimate", "invoice"} {
		p, err := f.svc.DocumentListPage(ctx, reader, kind, authorization.DocumentListFilter{}, authorization.Assigned, 1, 25)
		if err != nil || p.Total != 0 || len(p.Rows) != 0 {
			t.Fatalf("assigned document scope after loss: %+v %v", p, err)
		}
	}
}

func TestCollectionPagesEffectiveInvoiceStatus(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	var invoice, paidStatus int64
	financial, err := commercial.Accept(commercial.Input{TaxRate: "0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,status_id,title,invoice_date,due_date,line_items,financial,settlement_state) VALUES($1,$2,$3,$4,'Zero Invoice','2026-11-27','0001-01-01','[]',$5,'paid') RETURNING id`, f.company, f.customer, f.job, f.invoiceStatus, financial).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(f.pool).FinalizeInvoice(ctx, statusflow.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}, invoice); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT status_id FROM invoice_effective_status WHERE invoice_id=$1`, invoice).Scan(&paidStatus); err != nil {
		t.Fatal(err)
	}
	page, err := f.svc.DocumentListPage(ctx, f.actor, "invoice", authorization.DocumentListFilter{StatusID: paidStatus}, authorization.All, 1, 25)
	if err != nil || page.Total != 1 || page.Rows[0].ID != invoice || page.Rows[0].Category != "invoice:paid" || page.Rows[0].DueDate != "" {
		t.Fatalf("effective Invoice status/filter: %+v %v", page, err)
	}
}
