package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
)

func TestPDFKnownLegacySourceMatchesWithoutRewritingHistoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Legacy PDF source").SaveX(ctx)
	fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Legacy PDF"}, "line_items": {"[]"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id))
	path := fmt.Sprintf("/invoices/%d", id)
	fieldPost(t, web, cookie, path+"/pdf/save", url.Values{}, 303)
	original := requestBody(t, web, cookie, "GET", path+"/pdf", 200)
	// An installed version has a historical fingerprint, but all known source
	// facts and its bytes remain immutable. Comparison must use retained facts.
	var legacy int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO document_pdf_versions(company_id,document_type,document_id,source_token,source_snapshot,filename,content,generated_by,generated_at) SELECT company_id,document_type,document_id,'installed-fingerprint',source_snapshot-'source_format',filename,content,generated_by,generated_at FROM document_pdf_versions WHERE document_id=$1 RETURNING id`, id).Scan(&legacy))
	before := historyDurableRows(t, pool, []string{"document_pdf_versions"})
	if got := requestBody(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d", path, legacy), 200); got != original {
		t.Fatal("known legacy bytes changed")
	}
	historyEqual(t, "read does not reconstruct legacy source", historyDurableRows(t, pool, []string{"document_pdf_versions"}), before)
	var unknown int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO document_pdf_versions(company_id,document_type,document_id,source_token,source_snapshot,filename,content,generated_by) VALUES($1,'invoice',$2,'unknown','{}','retained.pdf',$3,$4) RETURNING id`, company, id, []byte(original), admin).Scan(&unknown))
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "Historical source is unavailable")
	expectStatus(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d", path, unknown), 409)
	if got := requestBody(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d&confirm_stale=1", path, unknown), 200); got != original {
		t.Fatal("unknown-source bytes changed")
	}
}

func TestPDFInstalledSourceUpgradePreservesFactsAndGrantsHTTP(t *testing.T) {
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, entry := range entries {
		if entry.Name() == "095_pdf_source_review.up.sql" {
			continue
		}
		raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		historyCheck(t, err)
		prior[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed customer").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Installed invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	retained := []byte("%PDF-1.3\nretained installed bytes\n%%EOF")
	// This is the installed v1 source shape, including bookkeeping fields and
	// absent anchor objects. No source is copied from current content at upgrade.
	_, err = pool.Exec(ctx, `INSERT INTO document_pdf_versions(company_id,document_type,document_id,source_token,source_snapshot,filename,content,generated_by,generated_at)
 SELECT d.company_id,'invoice',d.id,'installed-token',jsonb_build_object(
 'document',to_jsonb(d)-ARRAY['updated_at','financial'],
 'customer',jsonb_build_object('display_name',c.display_name,'company_name',c.company_name,'email',c.email,'phone',c.phone),
 'job',jsonb_build_object('job_type',NULL,'subtitle',NULL,'start_time',NULL,'due_date',NULL),
 'asset',jsonb_build_object('manufacturer',NULL,'model',NULL,'serial_number',NULL),
 'status',to_jsonb(s),'allocations',NULL,'credit_applications',NULL,
 'presentation',jsonb_build_object('business_name',cs.business_name,'address',cs.address,'city',cs.city,'state',cs.state,'zip',cs.zip,'phone',cs.phone,'email',cs.email,'tax_id',cs.tax_id,'invoice_color',cs.invoice_color,'invoice_footer',cs.invoice_footer,'invoice_logo_path',cs.invoice_logo_path,'pdf_show_line_item_descriptions',cs.pdf_show_line_item_descriptions,'timezone',cs.timezone,'date_format',cs.date_format,'invoice_payment_terms',d.terms)),
 'installed.pdf',$2,$3,'2026-01-02T03:04:05Z' FROM invoices d JOIN customers c ON c.id=d.customer_id JOIN company_settings cs ON cs.company_id=d.company_id LEFT JOIN statuses s ON s.id=d.status_id WHERE d.id=$1`, i.ID, retained, admin)
	historyCheck(t, err)
	before := historyDurableRows(t, pool, []string{"document_pdf_versions", "authorization_grants"})
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "upgrade preserves exact historical rows and grants", historyDurableRows(t, pool, []string{"document_pdf_versions", "authorization_grants"}), before)
	path := fmt.Sprintf("/invoices/%d/pdf", i.ID)
	if got := requestBody(t, web, cookie, "GET", path, 200); got != string(retained) {
		t.Fatal("upgrade substituted known retained bytes")
	}
	client.Invoice.UpdateOne(i).SetNotes("Later source change").SaveX(ctx)
	expectStatus(t, web, cookie, "GET", path, 404)
	if got := requestBody(t, web, cookie, "GET", path+"?version=1&confirm_stale=1", 200); got != string(retained) {
		t.Fatal("upgrade rewrote retained bytes")
	}
}
