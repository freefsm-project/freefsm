package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/settlement"
)

func TestPDFSourceIgnoresUnrenderedCustomFieldsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Source customer").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Internal reading"}, "object_type": {"invoice"}, "field_type": {"text"}}, 303)
	fieldPost(t, router, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Source invoice"}, "line_items": {"[]"}, "custom_field_1": {"private one"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id))
	path := fmt.Sprintf("/invoices/%d", id)
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
	original := requestBody(t, router, cookie, "GET", path+"/pdf", 200)
	commentPost(t, router, cookie, path+"/comments", url.Values{"content": {"Not rendered on the commercial document"}, "operation_id": {newOperationKey()}}, 200)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET invoice_prefix='FUTURE',email_auto_cc='office@example.test' WHERE company_id=$1`, company)
	historyCheck(t, err)
	// A non-rendered field changes the commercial revision, not the PDF source.
	_, err = pool.Exec(ctx, `UPDATE invoices SET custom_fields=jsonb_set(custom_fields,'{0,value}','"private two"'),updated_at=clock_timestamp() WHERE id=$1`, id)
	historyCheck(t, err)
	if current := requestBody(t, router, cookie, "GET", path+"/pdf", 200); current != original {
		t.Fatal("unrendered field replaced PDF")
	}
	client.Customer.UpdateOne(c).SetDisplayName("Rendered customer change").SaveX(ctx)
	expectStatus(t, router, cookie, "GET", path+"/pdf", 404)
}

func TestPDFSettlementRaceKeepsCoherentCapturedAmountsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Settlement race").SaveX(ctx)
	fieldPost(t, router, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Settlement race"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id))
	path := fmt.Sprintf("/invoices/%d", id)
	fieldPost(t, router, cookie, path+"/finalize", commercialReviewedValues(t, router, cookie, path, url.Values{}), 303)
	start := make(chan struct{})
	generated := make(chan int, 1)
	paid := make(chan error, 1)
	go func() {
		<-start
		r := httptest.NewRequest("POST", path+"/pdf/save", nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		generated <- w.Code
	}()
	go func() {
		<-start
		_, err := settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: id, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: "2026-01-02"})
		paid <- err
	}()
	close(start)
	status := <-generated
	historyCheck(t, <-paid)
	if status == 409 {
		expectStatus(t, router, cookie, "GET", path+"/pdf", 404)
		return
	}
	if status != 303 {
		t.Fatalf("concurrent generation=%d", status)
	}
	var version, allocated int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id,coalesce((SELECT sum((v->>'amount_cents')::bigint) FROM jsonb_array_elements(source_snapshot->'allocations') v WHERE NOT (v->>'reversed')::boolean),0) FROM document_pdf_versions WHERE document_id=$1`, id).Scan(&version, &allocated))
	if allocated != 0 && allocated != 2500 {
		t.Fatalf("incoherent allocation=%d", allocated)
	}
	pdf := requestBody(t, router, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d&confirm_stale=1", path, version), 200)
	command := exec.Command("pdftotext", "-layout", "-", "-")
	command.Stdin = strings.NewReader(pdf)
	text, err := command.CombinedOutput()
	historyCheck(t, err)
	due := "100.00"
	if allocated == 0 {
		assertNotContains(t, string(text), "$25.00")
	} else {
		due = "75.00"
		assertContains(t, string(text), "$25.00")
	}
	if !regexp.MustCompile(`Amount Due:\s+\$` + regexp.QuoteMeta(due)).Match(text) {
		t.Fatalf("captured allocation=%d disagrees with PDF: %s", allocated, text)
	}
}

func TestPDFRendererAndStorageFaultPublishNothingHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Fault customer").SaveX(ctx)
	fieldPost(t, router, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Saved before renderer failure"}, "line_items": {"[]"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id))
	path := fmt.Sprintf("/invoices/%d", id)
	logo := filepath.Join(t.TempDir(), "broken.png")
	historyCheck(t, os.WriteFile(logo, []byte("not a PNG"), 0600))
	_, err := pool.Exec(ctx, `UPDATE company_settings SET invoice_logo_path=$1 WHERE company_id=$2`, logo, company)
	historyCheck(t, err)
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 500)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Saved before renderer failure")
	_, err = pool.Exec(ctx, `UPDATE company_settings SET invoice_logo_path='' WHERE company_id=$1`, company)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_pdf_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected PDF storage failure'; END $$; CREATE TRIGGER fail_pdf_insert BEFORE INSERT ON document_pdf_versions FOR EACH STATEMENT EXECUTE FUNCTION fail_pdf_insert()`)
	historyCheck(t, err)
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 500)
	var versions, outbox int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions`).Scan(&versions))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries`).Scan(&outbox))
	if versions != 0 || outbox != 0 {
		t.Fatalf("fault published version=%d outbox=%d", versions, outbox)
	}
	expectStatus(t, router, cookie, "GET", path+"/pdf", 404)
}

func TestPDFRenderedCustomerRaceRejectsCapturedSourceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Accepted old customer").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Saved race invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	tx, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`)
	historyCheck(t, err)
	result := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", fmt.Sprintf("/invoices/%d/pdf/save", i.ID), nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		result <- w.Code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE a.application_name=$1 AND l.locktype='advisory' AND NOT l.granted)`, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&waiting))
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("PDF did not reach commit fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err = tx.Exec(ctx, `UPDATE customers SET display_name='New customer source' WHERE id=$1`, c.ID)
	historyCheck(t, err)
	historyCheck(t, tx.Commit(ctx))
	if status := <-result; status != 409 {
		t.Fatalf("source race status=%d", status)
	}
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/invoices/%d/pdf", i.ID), 404)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/invoices/%d", i.ID), 200), "Saved race invoice")
}
