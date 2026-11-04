package database

import (
	"io/fs"
	"os"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/testutil"
)

func TestCommercialFinancialUpgradePreservesLegacyAmountsAndPDF(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	db, ctx := fileMigrationDatabaseThrough046(t, dsn)
	a := seedFileOwnershipTenant(t, ctx, db, "financial-upgrade")
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() >= "085_" {
			continue
		}
		data, err := fs.ReadFile(MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		prior[entry.Name()] = &fstest.MapFile{Data: data}
	}
	if err := db.Migrate(ctx, prior); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO company_settings(company_id) VALUES($1)`, a.company); err != nil {
		t.Fatal(err)
	}
	var invoice int64
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,title,terms,invoice_number,tax_rate,line_items) VALUES($1,$2,'Legacy tax rounding','Recorded terms',1,'10','[{"title":"One","quantity":1,"unit_price":0.05,"taxable":true},{"title":"Two","quantity":1,"unit_price":0.05,"taxable":true}]') RETURNING id`, a.company, a.customer), &invoice)
	if _, err := db.Pool.Exec(ctx, `INSERT INTO document_pdf_versions(company_id,document_type,document_id,source_token,source_snapshot,filename,content,generated_by) VALUES($1,'invoice',$2,'recorded-source','{"legacy_total":"0.11"}','recorded.pdf',decode('255044462d7265636f72646564','hex'),$3)`, a.company, invoice, a.user); err != nil {
		t.Fatal(err)
	}
	const operation = "48000000-0000-4000-8000-000000000085"
	const intent = `{"operation_id":"48000000-0000-4000-8000-000000000085","job_id":0,"title":"Invalid legacy input","line_items":[{"title":"Retained input","quantity":1.5,"unit_price":"10.00","taxable":false}]}`
	const receipt = `{"operation_id":"48000000-0000-4000-8000-000000000085","outcome":"invalid_document"}`
	if _, err := db.Pool.Exec(ctx, `INSERT INTO commercial_operations(user_id,client_hash,operation_id,document_type,requested_id,intent,receipt) VALUES($1,'recorded-device',$2,'invoice',0,$3,$4)`, a.user, operation, intent, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO operation_admissions(operation_id,deployment_id,user_id,client_key,operation_type,intent) SELECT $1,id,$2,'api:recorded-device','commercial.invoice',jsonb_build_object('id',0,'intent',$3::jsonb) FROM operation_deployment`, operation, a.user, intent); err != nil {
		t.Fatal(err)
	}
	query := `SELECT jsonb_build_object('invoice',(SELECT (to_jsonb(i)-'financial'-'lease_id')||jsonb_build_object('tax_rate',i.tax_rate::text) FROM invoices i WHERE id=$1),'pdf',(SELECT jsonb_agg(to_jsonb(p)) FROM document_pdf_versions p),'numbers',(SELECT jsonb_agg(to_jsonb(n) ORDER BY document_type,number) FROM issued_document_numbers n),'operations',(SELECT jsonb_agg(to_jsonb(o)-'intent_version' ORDER BY operation_id) FROM commercial_operations o),'admissions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY operation_id) FROM operation_admissions a))::text`
	var before, after string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, query, invoice), &before)
	var oldPDFDocument, newPDFDocument string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT (to_jsonb(i)-'updated_at')::text FROM invoices i WHERE id=$1`, invoice), &oldPDFDocument)
	sequences := testutil.Sequences(t, ctx, db.Pool)
	for range 2 {
		if err := db.Migrate(ctx, MigrationFS()); err != nil {
			t.Fatal(err)
		}
	}
	mustMigrationScan(t, db.Pool.QueryRow(ctx, query, invoice), &after)
	// lease_id is new non-rendering provenance, not a recorded legacy fact.
	// Compare every old key and separately reject fabricated provenance.
	testutil.AssertNoInvoiceLeaseBackfill(t, ctx, db.Pool)
	testutil.AssertSequenceUpgrade(t, ctx, db.Pool, sequences, "asset_service_entries_id_seq", "lease_billing_obligations_id_seq", "leases_id_seq", "time_entry_history_id_seq", "lease_billing_source_changes_id_seq", "lease_billing_obligation_revisions_id_seq", "lease_coverage_events_id_seq", "lease_coverage_revisions_id_seq", "lease_notice_revisions_id_seq", "lease_fact_corrections_id_seq", "lease_billing_revisions_id_seq", "lease_manual_adjustments_id_seq")
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT ((to_jsonb(i)-ARRAY['updated_at','financial','lease_id'])||jsonb_build_object('tax_rate',tax_rate::numeric))::text FROM invoices i WHERE id=$1`, invoice), &newPDFDocument)
	if oldPDFDocument != newPDFDocument {
		t.Fatal("schema-only upgrade changed the legacy PDF document source projection")
	}
	if before != after {
		t.Fatal("upgrade changed legacy recorded fields, revision, identity or immutable PDF")
	}
	var raw string
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT financial FROM invoices WHERE id=$1`, invoice), &raw)
	if raw != "" {
		t.Fatal("upgrade invented an accepted arithmetic revision")
	}
	var version int
	mustMigrationScan(t, db.Pool.QueryRow(ctx, `SELECT intent_version FROM commercial_operations WHERE operation_id=$1`, operation), &version)
	if version != 0 {
		t.Fatal("upgrade changed historical request normalization")
	}
}
