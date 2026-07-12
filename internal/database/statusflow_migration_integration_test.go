package database

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStatusflowMigration044MapsHistoricalReferencesAndConstraintsIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("freefsm_status_migration_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`) })
	db, err := Connect(ctx, statusMigrationDSN(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	all := MigrationFS()
	entries, err := fs.ReadDir(all, ".")
	if err != nil {
		t.Fatal(err)
	}
	through043 := fstest.MapFS{}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() >= "044_" {
			continue
		}
		data, e := fs.ReadFile(all, entry.Name())
		if e != nil {
			t.Fatal(e)
		}
		through043[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err = db.Migrate(ctx, through043); err != nil {
		t.Fatal(err)
	}

	type tenant struct{ company, customer, jobWorkflow, estimateWorkflow, invoiceWorkflow, jobCustom, estimateCustom, invoicePaid int64 }
	seed := func(suffix string) tenant {
		var x tenant
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES($1,$2) RETURNING id`, "Tenant "+suffix, "status-"+suffix), &x.company)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, x.company), &x.customer)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Jobs','job') RETURNING id`, x.company), &x.jobWorkflow)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Estimates','estimate') RETURNING id`, x.company), &x.estimateWorkflow)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoices','invoice') RETURNING id`, x.company), &x.invoiceWorkflow)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO statuses(company_id,workflow_id,name,sort_order) VALUES($1,$2,'Waiting on permit',8) RETURNING id`, x.company, x.jobWorkflow), &x.jobCustom)
		_, _ = db.Pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,sort_order) VALUES($1,$2,'New',1),($1,$2,'Travel Time',2),($1,$2,'In Progress',3),($1,$2,'Completed',4),($1,$2,'Cancelled',5)`, x.company, x.jobWorkflow)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO statuses(company_id,workflow_id,name,sort_order) VALUES($1,$2,'Client review',9) RETURNING id`, x.company, x.estimateWorkflow), &x.estimateCustom)
		_, _ = db.Pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,sort_order) VALUES($1,$2,'Draft',1),($1,$2,'Sent',2),($1,$2,'Approved',3),($1,$2,'Declined',4),($1,$2,'Completed',5)`, x.company, x.estimateWorkflow)
		_, _ = db.Pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,sort_order) VALUES($1,$2,'Draft',1),($1,$2,'Sent',2),($1,$2,'Void',3),($1,$2,'Legacy custom',4)`, x.company, x.invoiceWorkflow)
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO statuses(company_id,workflow_id,name,sort_order) VALUES($1,$2,'Paid',5) RETURNING id`, x.company, x.invoiceWorkflow), &x.invoicePaid)
		return x
	}
	a := seed("a")
	b := seed("b")
	var sparseCompany, sparseCustomer, sparseWorkflow, sparseDraft, sparsePaid int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Sparse invoice tenant','status-sparse') RETURNING id`), &sparseCompany)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, sparseCompany), &sparseCustomer)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoices','invoice') RETURNING id`, sparseCompany), &sparseWorkflow)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO statuses(company_id,workflow_id,name,sort_order) VALUES($1,$2,'Draft',1) RETURNING id`, sparseCompany, sparseWorkflow), &sparseDraft)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO statuses(company_id,workflow_id,name,sort_order) VALUES($1,$2,'Paid',2) RETURNING id`, sparseCompany, sparseWorkflow), &sparsePaid)
	var activeJob, archivedJob, tombstoneEstimate, invoice int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,status_id) VALUES($1,$2,'Active',$3) RETURNING id`, a.company, a.customer, a.jobCustom), &activeJob)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type,status_id,deleted_at) VALUES($1,$2,'Archived',$3,now()) RETURNING id`, a.company, a.customer, a.jobCustom), &archivedJob)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,status_id,title) VALUES($1,$2,$3,'Hidden') RETURNING id`, a.company, a.customer, a.estimateCustom), &tombstoneEstimate)
	_, _ = db.Pool.Exec(ctx, `ALTER TABLE estimates DISABLE TRIGGER estimate_conversion_tombstone_guard`)
	_, _ = db.Pool.Exec(ctx, `UPDATE estimates SET conversion_hidden_at=now() WHERE id=$1`, tombstoneEstimate)
	_, _ = db.Pool.Exec(ctx, `ALTER TABLE estimates ENABLE TRIGGER estimate_conversion_tombstone_guard`)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,status_id,invoice_number,title) VALUES($1,$2,$3,42,'Legacy paid') RETURNING id`, a.company, a.customer, a.invoicePaid), &invoice)
	var sparseUnpaidInvoice, sparsePaidInvoice int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,status_id,invoice_number,title,settlement_state) VALUES($1,$2,$3,43,'Sparse unpaid','unpaid') RETURNING id`, sparseCompany, sparseCustomer, sparsePaid), &sparseUnpaidInvoice)
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,status_id,invoice_number,title,settlement_state) VALUES($1,$2,$3,44,'Sparse paid','paid') RETURNING id`, sparseCompany, sparseCustomer, sparsePaid), &sparsePaidInvoice)

	sql, err := fs.ReadFile(all, "044_category_status_workflows.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("apply 044: %v", err)
	}
	for _, id := range []int64{activeJob, archivedJob} {
		var key string
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT s.category_key FROM jobs j JOIN statuses s ON s.id=j.status_id WHERE j.id=$1`, id), &key)
		if key != "job:pending" {
			t.Fatalf("job %d category=%s", id, key)
		}
	}
	var estimateKey string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT s.category_key FROM estimates e JOIN statuses s ON s.id=e.status_id WHERE e.id=$1 AND e.conversion_hidden_at IS NOT NULL`, tombstoneEstimate), &estimateKey)
	if estimateKey != "estimate:estimate" {
		t.Fatalf("tombstone category=%s", estimateKey)
	}
	var invoiceKey string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT s.category_key FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.id=$1`, invoice), &invoiceKey)
	if invoiceKey != "invoice:sent" {
		t.Fatalf("legacy paid assignment=%s", invoiceKey)
	}
	for _, tc := range []struct {
		id        int64
		effective string
	}{{sparseUnpaidInvoice, "invoice:sent"}, {sparsePaidInvoice, "invoice:paid"}} {
		var underlying, effective string
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT manual.category_key,effective.category_key FROM invoices i JOIN statuses manual ON manual.id=i.status_id JOIN invoice_effective_status v ON v.invoice_id=i.id JOIN statuses effective ON effective.id=v.status_id WHERE i.id=$1`, tc.id), &underlying, &effective)
		if underlying != "invoice:sent" || effective != tc.effective {
			t.Fatalf("sparse invoice %d underlying/effective=%s/%s", tc.id, underlying, effective)
		}
	}
	var preservedDraft int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='invoice:draft'`, sparseWorkflow), &preservedDraft)
	if preservedDraft != sparseDraft {
		t.Fatalf("sparse draft id changed: got %d want %d", preservedDraft, sparseDraft)
	}
	for _, company := range []int64{a.company, b.company} {
		var bad int
		mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT s.category_key,count(*) FILTER(WHERE s.is_category_default) defaults FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.company_id=$1 GROUP BY s.workflow_id,s.category_key HAVING count(*) FILTER(WHERE s.is_category_default)<>1) x`, company), &bad)
		if bad != 0 {
			t.Fatalf("company %d bad defaults=%d", company, bad)
		}
	}
	var projectDefault string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT s.name FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.object_type='project' AND s.category_key='project:new' AND s.is_category_default ORDER BY w.id LIMIT 1`), &projectDefault)
	if projectDefault != "Opportunity" {
		t.Fatalf("project new default=%q", projectDefault)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) SELECT company_id,id,' waiting on permit ','job:pending',99,false FROM status_workflows WHERE id=$1`, a.jobWorkflow); err == nil {
		t.Fatal("case-insensitive duplicate label accepted")
	}
}

type migrationScanner interface{ Scan(...any) error }

func mustMigrationScan(t *testing.T, row migrationScanner, dest ...any) {
	t.Helper()
	if err := row.Scan(dest...); err != nil {
		t.Fatal(err)
	}
}
func statusMigrationDSN(t *testing.T, dsn, schema string) string {
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
