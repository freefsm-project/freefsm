package handlers

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5/pgxpool"
)

func conversionReview(t *testing.T, web http.Handler, cookie *http.Cookie, path string) url.Values {
	t.Helper()
	form := requestBody(t, web, cookie, "GET", path, 200)
	values := url.Values{"idempotency_key": {newOperationKey()}, "revision": {compositionField(t, form, "revision")}}
	if strings.HasSuffix(path, "/revert-to-estimate") {
		values.Set("cycle_id", compositionField(t, form, "cycle_id"))
	}
	return values
}

func conversionUpload(t *testing.T, web http.Handler, cookie *http.Cookie, kind string, id int64, name, content string) {
	t.Helper()
	var body bytes.Buffer
	m := multipart.NewWriter(&body)
	historyCheck(t, m.WriteField("publication_id", newOperationKey()))
	f, err := m.CreateFormFile("file", name)
	historyCheck(t, err)
	_, err = f.Write([]byte(content))
	historyCheck(t, err)
	historyCheck(t, m.Close())
	r := httptest.NewRequest("POST", fmt.Sprintf("/files?object_type=%s&object_id=%d", kind, id), &body)
	r.Header.Set("Content-Type", m.FormDataContentType())
	r.Header.Set("HX-Request", "true")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	web.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("upload=%d %s", w.Code, w.Body.String())
	}
}

func conversionRelations(t *testing.T, pool *pgxpool.Pool, kind string, id int64) string {
	t.Helper()
	var value string
	historyCheck(t, pool.QueryRow(context.Background(), `SELECT jsonb_build_array(
 (SELECT coalesce(jsonb_agg(to_jsonb(f)-ARRAY['object_type','object_id','revision'] ORDER BY id),'[]') FROM files f WHERE object_type=$1 AND object_id=$2),
 (SELECT coalesce(jsonb_agg(to_jsonb(c)-ARRAY['object_type','object_id'] ORDER BY id),'[]') FROM comments c WHERE object_type=$1 AND object_id=$2),
 (SELECT coalesce(jsonb_agg(to_jsonb(t)-ARRAY['object_type','object_id'] ORDER BY id),'[]') FROM tag_links t WHERE object_type=$1 AND object_id=$2))::text`, kind, id).Scan(&value))
	return value
}

type conversionHistory struct {
	estimate, first, current int64
	commands                 []creditCommand
}

// Drives the installed HTTP surfaces. The same scenario is used before a real
// encrypted backup, so restoration is verified against real command receipts,
// immutable PDFs/deliveries, field captures and durable uploaded bytes.
func exerciseConversionHistory(t *testing.T, pool *pgxpool.Pool, client *ent.Client, web http.Handler, cookie *http.Cookie, company int64) conversionHistory {
	t.Helper()
	ctx := context.Background()
	out := conversionHistory{}
	for _, d := range []struct{ kind, name, key, def string }{
		{"estimate", "Source label", "shared", ""}, {"invoice", "Different Invoice label", "shared", ""},
		// Identical labels without keys deliberately remain document-only.
		{"estimate", "Unpaired label", "", ""}, {"invoice", "Unpaired label", "", "Fresh Invoice default"},
	} {
		fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"object_type": {d.kind}, "name": {d.name}, "field_type": {"text"}, "conversion_key": {d.key}, "default_value": {d.def}}, 303)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Conversion inventory Customer").SaveX(ctx)
	fieldPost(t, web, cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Conversion inventory"}, "line_items": {"[]"}, "custom_field_1": {"Original shared"}, "custom_field_3": {"Original Estimate-only"}}, 303)
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates WHERE title='Conversion inventory'`).Scan(&out.estimate))
	source := fmt.Sprintf("/estimates/%d", out.estimate)
	fieldPost(t, web, cookie, source+"/comments", url.Values{"content": {"Original Comment"}}, 200)
	conversionUpload(t, web, cookie, "estimate", out.estimate, "source.txt", "Original file bytes")
	fieldPost(t, web, cookie, "/tags", url.Values{"name": {"Conversion inventory tag"}}, 303)
	var tag int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM tags WHERE name='Conversion inventory tag'`).Scan(&tag))
	fieldPost(t, web, cookie, fmt.Sprintf("%s/tags/%d/attach", source, tag), url.Values{}, 200)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='localhost',smtp_from='conversion@example.test' WHERE company_id=$1`, company)
	historyCheck(t, err)
	pdfDelivery := func(path, kind string, id int64) {
		t.Helper()
		fieldPost(t, web, cookie, path+"/pdf/save", url.Values{}, 303)
		var version int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT max(id) FROM document_pdf_versions WHERE document_type=$1 AND document_id=$2`, kind, id).Scan(&version))
		fieldPost(t, web, cookie, path+"/email", url.Values{"pdf_version": {fmt.Sprint(version)}, "idempotency_key": {newOperationKey()}, "to": {"recipient@example.test"}, "subject": {"Retained original identity"}, "body": {"Original delivery text"}}, 303)
	}
	pdfDelivery(source, "estimate", out.estimate)
	failAudit := func(action string) {
		t.Helper()
		_, err := pool.Exec(ctx, `CREATE FUNCTION inventory_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action=TG_ARGV[0] THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='injected post-transfer audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER inventory_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION inventory_audit_failure('`+action+`')`)
		historyCheck(t, err)
	}
	clearFault := func() {
		t.Helper()
		_, err := pool.Exec(ctx, `DROP TRIGGER inventory_audit_failure ON authorization_audit; DROP FUNCTION inventory_audit_failure()`)
		historyCheck(t, err)
	}
	fixed := []string{"document_pdf_versions", "document_deliveries", "document_delivery_events"}
	beforePDF := historyDurableRows(t, pool, fixed)
	before := conversionRelations(t, pool, "estimate", out.estimate)
	command := creditCommand{source + "/convert", conversionReview(t, web, cookie, source+"/convert")}
	failAudit("estimate.convert")
	fieldPost(t, web, cookie, command.path, command.values, 500)
	expectStatus(t, web, cookie, "GET", "/commercial-operations/"+command.values.Get("idempotency_key"), 404)
	if got := conversionRelations(t, pool, "estimate", out.estimate); got != before {
		t.Fatal("failed conversion partially transferred collaboration")
	}
	var failedCycles int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM estimate_invoice_conversion_cycles`).Scan(&failedCycles))
	if failedCycles != 0 {
		t.Fatal("failed conversion left a cycle")
	}
	historyEqual(t, "failed conversion rolls back cancellation and every original PDF/Delivery field", historyDurableRows(t, pool, fixed), beforePDF)
	clearFault()
	var conversionStarted time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&conversionStarted))
	fieldPost(t, web, cookie, command.path, command.values, 303)
	out.commands = append(out.commands, command)
	historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1 AND reverted_at IS NULL`, out.estimate).Scan(&out.first))
	invoice := fmt.Sprintf("/invoices/%d", out.first)
	expectStatus(t, web, cookie, "GET", source, 403)
	fieldPost(t, web, cookie, source+"/restore", nil, 404)
	assertNotContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/customers/%d/work?type=estimate&archived=1", c.ID), 200), source+`"`)
	assertNotContains(t, requestBody(t, web, cookie, "GET", "/estimates", 200), source+`"`)
	assertNotContains(t, requestBody(t, web, cookie, "GET", "/search?q=Conversion", 200), source)
	fieldPost(t, web, cookie, source+"/custom-fields", url.Values{"custom_field_1": {"Must not mutate hidden source"}}, 404)
	if _, err := pool.Exec(ctx, `UPDATE estimates SET title='tamper' WHERE id=$1`, out.estimate); err == nil {
		t.Fatal("database allowed hidden source mutation")
	}
	if got := conversionRelations(t, pool, "invoice", out.first); got != before {
		t.Fatalf("conversion changed stable collaboration\nbefore %s\nafter %s", before, got)
	}
	if got := conversionRelations(t, pool, "estimate", out.estimate); got != "[[], [], []]" {
		t.Fatal("source retains current collaboration", got)
	}
	beforePDF = conversionCancelledDeliveries(t, pool, beforePDF, "estimate", out.estimate, conversionStarted)
	fieldPost(t, web, cookie, invoice+"/custom-fields", url.Values{"custom_field_2": {"Current shared"}, "custom_field_4": {"First Invoice-only changed"}}, 303)
	fieldPost(t, web, cookie, invoice+"/comments", url.Values{"content": {"Current Invoice Comment"}}, 200)
	conversionUpload(t, web, cookie, "invoice", out.first, "invoice.txt", "Current Invoice file bytes")
	pdfDelivery(invoice, "invoice", out.first)
	beforePDF = historyDurableRows(t, pool, fixed)
	before = conversionRelations(t, pool, "invoice", out.first)
	command = creditCommand{invoice + "/revert-to-estimate", conversionReview(t, web, cookie, invoice+"/revert-to-estimate")}
	failAudit("invoice.revert")
	fieldPost(t, web, cookie, command.path, command.values, 500)
	expectStatus(t, web, cookie, "GET", "/commercial-operations/"+command.values.Get("idempotency_key"), 404)
	if got := conversionRelations(t, pool, "invoice", out.first); got != before {
		t.Fatal("failed reversion partially transferred current collaboration")
	}
	var active bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT reverted_at IS NULL FROM estimate_invoice_conversion_cycles WHERE invoice_id=$1`, out.first).Scan(&active))
	if !active {
		t.Fatal("failed reversion closed cycle")
	}
	historyEqual(t, "failed reversion rolls back cancellation and every original PDF/Delivery field", historyDurableRows(t, pool, fixed), beforePDF)
	clearFault()
	historyCheck(t, pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&conversionStarted))
	fieldPost(t, web, cookie, command.path, command.values, 303)
	out.commands = append(out.commands, command)
	expectStatus(t, web, cookie, "GET", invoice, 403)
	fieldPost(t, web, cookie, invoice+"/restore", nil, 404)
	assertNotContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/customers/%d/work?type=invoice&archived=1", c.ID), 200), invoice+`"`)
	assertNotContains(t, requestBody(t, web, cookie, "GET", "/invoices", 200), invoice+`"`)
	assertNotContains(t, requestBody(t, web, cookie, "GET", "/search?q=Conversion", 200), invoice)
	if got := conversionRelations(t, pool, "estimate", out.estimate); got != before {
		t.Fatalf("reversion changed current collaboration\nbefore %s\nafter %s", before, got)
	}
	if got := conversionRelations(t, pool, "invoice", out.first); got != "[[], [], []]" {
		t.Fatal("hidden Invoice retains current collaboration", got)
	}
	beforePDF = conversionCancelledDeliveries(t, pool, beforePDF, "invoice", out.first, conversionStarted)
	noResurrection := func() {
		t.Helper()
		sender := &compositionSender{}
		worked, err := delivery.New(pool, "").ProcessOne(ctx, sender, nil)
		historyCheck(t, err)
		if worked || len(sender.messages) != 0 {
			t.Fatal("conversion/reversion resurrected or retargeted original mail")
		}
		historyEqual(t, "worker leaves cancelled original inventory unchanged", historyDurableRows(t, pool, fixed), beforePDF)
	}
	noResurrection()
	var restored, hidden string
	historyCheck(t, pool.QueryRow(ctx, `SELECT custom_fields::text FROM estimates WHERE id=$1`, out.estimate).Scan(&restored))
	historyCheck(t, pool.QueryRow(ctx, `SELECT custom_fields::text FROM invoices WHERE id=$1`, out.first).Scan(&hidden))
	for _, value := range []string{"Current shared", "Original Estimate-only"} {
		assertContains(t, restored, value)
	}
	assertNotContains(t, restored, "First Invoice-only changed")
	assertContains(t, hidden, "First Invoice-only changed")
	command = creditCommand{source + "/convert", conversionReview(t, web, cookie, source+"/convert")}
	fieldPost(t, web, cookie, command.path, command.values, 303)
	out.commands = append(out.commands, command)
	historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1 AND reverted_at IS NULL`, out.estimate).Scan(&out.current))
	if out.current == out.first {
		t.Fatal("reused Invoice identity")
	}
	if got := conversionRelations(t, pool, "invoice", out.current); got != before {
		t.Fatal("reconversion lost or duplicated collaboration")
	}
	var current string
	historyCheck(t, pool.QueryRow(ctx, `SELECT custom_fields::text FROM invoices WHERE id=$1`, out.current).Scan(&current))
	assertContains(t, current, "Current shared")
	assertContains(t, current, "Fresh Invoice default")
	assertNotContains(t, current, "Original Estimate-only")
	assertNotContains(t, current, "First Invoice-only changed")
	historyEqual(t, "PDF/Delivery bytes and identities over all cycles", historyDurableRows(t, pool, fixed), beforePDF)
	currentPath := fmt.Sprintf("/invoices/%d", out.current)
	fieldPost(t, web, cookie, currentPath+"/finalize", commercialReviewedValues(t, web, cookie, currentPath, url.Values{}), 303)
	fieldPost(t, web, cookie, currentPath+"/delete", url.Values{}, 303)
	historyEqual(t, "archive cannot alter or retarget the original cancelled inventory", historyDurableRows(t, pool, fixed), beforePDF)
	fieldPost(t, web, cookie, currentPath+"/restore", url.Values{}, 303)
	noResurrection()
	for _, name := range []string{"source.txt", "invoice.txt"} {
		var id int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM files WHERE original_name=$1`, name).Scan(&id))
		content := requestBody(t, web, cookie, "GET", fmt.Sprintf("/files/%d", id), 200)
		if name == "source.txt" {
			assertContains(t, content, "Original file bytes")
		} else {
			assertContains(t, content, "Current Invoice file bytes")
		}
	}
	return out
}

func TestConversionCollaborationAndOriginalDeliveryInventoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	exerciseConversionHistory(t, pool, client, web, sessionCookie(t, context.Background(), sessions, admin), company)
}
