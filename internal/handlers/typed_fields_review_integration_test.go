package handlers

import (
	"context"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestTypedReviewDocumentEditorsUseCurrentGrantsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Document customer").SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, adminCookie, "/settings/custom-fields", url.Values{"object_type": {"estimate"}, "name": {"Estimate secret"}, "field_type": {"text"}}, 303)
	fieldPost(t, router, adminCookie, "/settings/custom-fields", url.Values{"object_type": {"invoice"}, "name": {"Invoice secret"}, "field_type": {"text"}}, 303)
	estimate := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Estimate").SetCustomFields(`[{"definition_id":1,"value":"ESTIMATE_FIELD_PROBE"}]`).SaveX(ctx)
	invoice := client.Invoice.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetInvoiceNumber(29).SetSettlementState("paid").SetTitle("Invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetCustomFields(`[{"definition_id":2,"value":"INVOICE_FIELD_PROBE"}]`).SaveX(ctx)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Customer only", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("legacy-admin-editor@test").SetName("Legacy admin").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	for _, path := range []string{fmt.Sprintf("/estimates/%d/edit", estimate.ID), fmt.Sprintf("/invoices/%d/edit", invoice.ID)} {
		body := requestBody(t, router, cookie, "GET", path, 404)
		assertNotContains(t, body, "FIELD_PROBE")
	}
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "pricing.view", Scope: authorization.All}, {Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.edit", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.edit", Scope: authorization.All}}
	role, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Renamed document editor", grants)
	if err != nil {
		t.Fatal(err)
	}
	tech := client.User.Create().SetCompanyID(company).SetEmail("legacy-tech-editor@test").SetName("Editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, tech.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie = sessionCookie(t, ctx, sessions, tech.ID)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d/edit", estimate.ID), 200), "ESTIMATE_FIELD_PROBE")
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/invoices/%d/edit", invoice.ID), 200), "INVOICE_FIELD_PROBE")
	fieldPost(t, router, cookie, fmt.Sprintf("/estimates/%d", estimate.ID), url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Edited estimate"}, "line_items": {"[]"}, "tax_rate": {"0"}, "custom_field_1": {"Allowed estimate edit"}}, 303)
	fieldPost(t, router, cookie, fmt.Sprintf("/invoices/%d", invoice.ID), url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "invoice_number": {fmt.Sprint(invoice.InvoiceNumber)}, "title": {"Edited invoice"}, "line_items": {"[]"}, "tax_rate": {"0"}, "custom_field_2": {"Allowed invoice edit"}}, 303)
}

func TestTypedReviewConfigurationFailsClosedWithoutActorHTTP(t *testing.T) {
	_, client, router, sessions, _, admin := permissionHTTPFixture(t)
	h := NewCustomFieldHandler(services.NewCustomFieldDefinitionService(client))
	r := httptest.NewRequest("POST", "/settings/custom-fields", strings.NewReader(url.Values{"object_type": {"customer"}, "field_type": {"unknown"}, "name": {"Must not persist"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.Create(w, r)
	if w.Code != 403 {
		t.Fatalf("missing actor accepted by production handler: %d %s", w.Code, w.Body.String())
	}
	for _, fn := range []http.HandlerFunc{h.List, h.Update, h.Delete} {
		w := httptest.NewRecorder()
		fn(w, httptest.NewRequest("GET", "/settings/custom-fields/1", nil))
		if w.Code != 403 {
			t.Fatalf("missing actor got %d", w.Code)
		}
	}
	body := requestBody(t, router, sessionCookie(t, context.Background(), sessions, admin), "GET", "/settings/custom-fields", 200)
	assertNotContains(t, body, "Must not persist")
}

func TestTypedReviewMixedPlanningAndFieldScopesHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Planner and assigned field editor", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.All}, {Permission: "job.edit", Scope: authorization.All}, {Permission: "job.fields.edit", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("mixed-scopes@test").SetName("Planner").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Scope customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Scope Job").SaveX(ctx)
	fieldPost(t, router, sessionCookie(t, ctx, sessions, admin), "/settings/custom-fields", url.Values{"object_type": {"job"}, "name": {"Flag"}, "field_type": {"checkbox"}}, 303)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	input := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "form_mode": {"edit"}, "job_type": {"Changed title"}, "custom_field_1": {"false"}}
	fieldPost(t, router, cookie, path, input, 403)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	fieldPost(t, router, cookie, path, input, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/custom-fields", 200), `value="false" selected`)
	input.Set("assignments", "[]")
	input.Set("job_type", "Must not self-remove scope")
	input.Set("custom_field_1", "true")
	fieldPost(t, router, cookie, path, input, 403)
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/custom-fields", 200), `value="false" selected`)
}

func TestTypedReviewOmittedFieldPatchStillValidatesDetailsButNotSubtaskHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Details customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Original details").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"object_type": {"job"}, "name": {"New required details"}, "field_type": {"text"}, "required": {"true"}}, 303)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	fieldPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "form_mode": {"edit"}, "job_type": {"Must not persist"}}, 422)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Original details")
	assertNotContains(t, body, "Must not persist")
	fieldPost(t, router, cookie, path+"/subtasks", url.Values{"action": {"add"}, "title": {"Independent task"}, "operation_id": {newOperationKey()}}, 303)
	check, err := authorization.New(pool).Subtasks(ctx, authorization.Actor{UserID: admin}, j.ID)
	if err != nil || len(check.Entries) != 1 {
		t.Fatalf("checklist: %+v %v", check, err)
	}
	fieldPost(t, router, cookie, path+"/subtasks", url.Values{"action": {"complete"}, "subtask_id": {check.Entries[0].ID}, "completion_revision": {check.Entries[0].CompletionRevision}, "operation_id": {newOperationKey()}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Independent task")
}

func TestTypedReviewParentViewDoesNotAcquireMutationFenceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Read customer").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Unblocked project view").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r := httptest.NewRequest("GET", fmt.Sprintf("/projects/%d", p.ID), nil).WithContext(readCtx)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 || readCtx.Err() != nil {
		t.Fatalf("read-only parent view waited for mutation fence: %d %v", w.Code, readCtx.Err())
	}
	assertContains(t, w.Body.String(), "Unblocked project view")
}

func TestTypedReviewCorrectionRetainsCompleteDomainSubmissionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Correction customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Before correction").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"object_type": {"job"}, "name": {"Required safety"}, "field_type": {"checkbox"}, "required": {"true"}}, 303)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	input := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "form_mode": {"edit"}, "job_type": {"Corrected domain details"}, "notes": {"Preserve multi\nline notes"}, "visits": {`[{"date":"2026-10-20","start_time":"09:00","end_time":"10:00","notes":"Retained visit"}]`}, "assignments": {"[]"}, "subtasks": {`[{"title":"Retained task","completed":false}]`}, "start_time": {"2026-10-20T09:00"}, "end_time": {"2026-10-20T10:00"}, "due_date": {"2026-10-21"}, "status_id": {"123"}, "domain_extension": {"first", "second"}}
	failed := fieldPost(t, router, cookie, path, input, 422)
	// These are the successful controls of the returned correction form, ready
	// for a browser resubmit; structured domain fields must not become textareas.
	controls := url.Values{}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(failed.Body.String(), -1) {
		controls.Add(html.UnescapeString(m[1]), html.UnescapeString(m[2]))
	}
	for key, want := range input {
		got := controls[key]
		if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("correction lost %s: %q want %q", key, got, want)
		}
	}
	controls.Set("custom_field_1", "false")
	fieldPost(t, router, cookie, path, controls, 303)
	body := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	assertContains(t, body, "Corrected domain details")
	assertContains(t, body, "Retained task")
	assertContains(t, body, "Retained visit")
	if !strings.Contains(body, "2026-10-21") {
		t.Fatalf("due date after correction: %s", regexp.MustCompile(`name="due_date"[^>]+`).FindString(body))
	}
}

func TestTypedReviewLegacyPairedHiddenSnapshotsSurviveUpgradeHTTP(t *testing.T) {
	source := database.MigrationFS()
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		// Keep independent integrated schemas current while exercising the
		// typed-field upgrade against legacy definitions and conversion history.
		if entry.Name() != "058_typed_custom_fields.up.sql" {
			data, err := fs.ReadFile(source, entry.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[entry.Name()] = &fstest.MapFile{Data: data}
		}
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `INSERT INTO custom_field_definitions(id,company_id,object_type,name,field_type,options,conversion_key) VALUES(900,$1,'estimate','Legacy estimate selection','select','[" North ","South"]','legacy-pair'),(901,$1,'invoice','Legacy invoice selection','select','[" North ","South"]','legacy-pair')`, company); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Legacy paired customer").SaveX(ctx)
	e1 := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("First legacy cycle").SetCustomFields(`[{"definition_id":900,"value":" North "}]`).SaveX(ctx)
	e2 := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Second legacy cycle").SetCustomFields(`[{"definition_id":900,"value":"South"}]`).SaveX(ctx)
	// Seed recorded pre-v1 cycles rather than running today's conversion code
	// against a historical schema. No field-pair evidence existed in these rows.
	legacyCycle := func(estimate int64, number int, reverted bool) string {
		t.Helper()
		var invoice int64
		if err := pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title,custom_fields) SELECT company_id,customer_id,$2,title,jsonb_build_array(jsonb_build_object('definition_id',901,'value',custom_fields->0->'value')) FROM estimates WHERE id=$1 RETURNING id`, estimate, number).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		cycle := fmt.Sprintf("29000000-0000-4000-8000-%012d", number)
		if _, err := pool.Exec(ctx, `INSERT INTO estimate_invoice_conversion_cycles(id,company_id,estimate_id,invoice_id,invoice_number,source_snapshot,converted_by) SELECT $1,company_id,id,$2,$3,to_jsonb(e),$4 FROM estimates e WHERE id=$5`, cycle, invoice, number, admin, estimate); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE estimates SET conversion_hidden_at=now() WHERE id=$1`, estimate); err != nil {
			t.Fatal(err)
		}
		if reverted {
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.conversion_revert_cycle',$1,true)`, cycle); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE estimate_invoice_conversion_cycles SET reverted_at=now(),reverted_by=converted_by WHERE id=$1`, cycle); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE invoices SET conversion_hidden_at=now() WHERE id=$1`, invoice); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(ctx, `UPDATE estimates SET conversion_hidden_at=NULL WHERE id=$1`, estimate); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return fmt.Sprintf("/invoices/%d", invoice)
	}
	firstInvoice := legacyCycle(e1.ID, 901, true)
	secondInvoice := legacyCycle(e2.ID, 902, false)
	// Compare the actual legacy identities, option associations, both kinds of
	// hidden document, immutable source snapshots and conversion receipts.
	inventory := func() string {
		t.Helper()
		var raw string
		err := pool.QueryRow(ctx, `SELECT jsonb_build_object('definitions',(SELECT jsonb_agg(to_jsonb(d)-'precision'-'default_value' ORDER BY id) FROM custom_field_definitions d),'estimates',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM estimates e),'invoices',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i),'cycles',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM estimate_invoice_conversion_cycles c),'operations',(SELECT jsonb_agg(to_jsonb(o) ORDER BY idempotency_key) FROM estimate_invoice_conversion_operations o))::text`).Scan(&raw)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := inventory()
	if err = (&database.DB{Pool: pool}).Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	if after := inventory(); after != before {
		t.Fatal("upgrade changed legacy paired records or immutable conversion history")
	}
	expectStatus(t, router, cookie, "GET", firstInvoice+"/custom-fields", 404)
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d/custom-fields", e2.ID), 404)
	assertContains(t, requestBody(t, router, cookie, "GET", secondInvoice+"/custom-fields", 200), `value="South" selected`)
	if _, err = pool.Exec(ctx, `UPDATE estimate_invoice_conversion_cycles SET source_snapshot=jsonb_set(source_snapshot,'{custom_fields}','[]')`); err == nil {
		t.Fatal("migration weakened immutable source snapshots")
	}
	if after := inventory(); after != before {
		t.Fatal("failed snapshot update changed history")
	}
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"object_type": {"estimate"}, "name": {"Required since upgrade"}, "field_type": {"checkbox"}, "required": {"true"}}, 303)
	fieldPost(t, router, cookie, secondInvoice+"/revert-to-estimate", url.Values{"idempotency_key": {"29000004-1111-4111-8111-111111111111"}}, 409)
	assertContains(t, requestBody(t, router, cookie, "GET", secondInvoice+"/custom-fields", 200), `value="South" selected`)
	fieldPost(t, router, cookie, "/settings/custom-fields/901", url.Values{"name": {"Legacy invoice selection"}, "field_type": {"select"}, "options": {`["South"," North "]`}}, 422)
	reviewPath := "/settings/custom-fields/cycles/29000000-0000-4000-8000-000000000902/review"
	assertContains(t, requestBody(t, router, cookie, "GET", reviewPath, 200), "Historical pairing review")
	evidence := `{"field_pairs":{"900":901},"field_definitions":[{"id":900,"name":"Legacy estimate selection","field_type":"select","options":"[\" North \",\"South\"]"},{"id":901,"name":"Legacy invoice selection","field_type":"select","options":"[\" North \",\"South\"]"}]}`
	beforeReview := inventory()
	fieldPost(t, router, cookie, reviewPath, url.Values{"evidence": {strings.Replace(evidence, `"field_pairs":{"900":901}`, `"field_pairs":{"900":999}`, 1)}, "reason": {"Invalid pairing must not commit"}, "reviewed": {"true"}}, 422)
	fieldPost(t, router, cookie, reviewPath, url.Values{"evidence": {evidence}, "reason": {"Reviewed original field configuration evidence"}, "reviewed": {"true"}}, 303)
	if after := inventory(); after != beforeReview {
		t.Fatal("retrospective review rewrote original cycle evidence")
	}
	fieldPost(t, router, cookie, reviewPath, url.Values{"evidence": {evidence}, "reason": {"Cannot replace accepted evidence"}, "reviewed": {"true"}}, 409)
	fieldPost(t, router, cookie, secondInvoice+"/revert-to-estimate", url.Values{"idempotency_key": {"29000005-1111-4111-8111-111111111111"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d/custom-fields", e2.ID), 200), `value="South" selected`)
}

func TestTypedReviewJobArchiveWaitsForAssignedDocumentSaveHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	u := client.User.Create().SetCompanyID(company).SetEmail("archive-document-race@test").SetName("Assigned editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Archive race").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetInvoiceNumber(29).SetTitle("Assigned invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetSettlementState("paid").SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	fieldPost(t, router, adminCookie, "/settings/custom-fields", url.Values{"object_type": {"invoice"}, "name": {"Invoice field"}, "field_type": {"text"}}, 303)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION hold_document_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='invoice.edit' THEN PERFORM pg_advisory_xact_lock(190129); END IF; RETURN NEW; END $$; CREATE TRIGGER hold_document_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION hold_document_audit()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(190129)`); err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	async := func(target string, cookie *http.Cookie, values url.Values) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			r := httptest.NewRequest("POST", target, strings.NewReader(values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			done <- w
		}()
		return done
	}
	path := fmt.Sprintf("/invoices/%d/custom-fields", i.ID)
	save := async(path, cookie, url.Values{"custom_field_1": {"Committed before archive"}})
	deadline := time.Now().Add(10 * time.Second)
	savePID := 0
	for savePID == 0 {
		if err := pool.QueryRow(ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`, blockerPID).Scan(&savePID); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("document save did not reach audit barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	archive := async(fmt.Sprintf("/jobs/%d/delete", j.ID), adminCookie, url.Values{})
	for {
		select {
		case response := <-archive:
			t.Fatalf("archive crossed an uncommitted document edit: %d %s", response.Code, response.Body.String())
		default:
		}
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, savePID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("archive did not wait for Job scope lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if response := <-save; response.Code != 303 {
		t.Fatalf("document save failed: %d %s", response.Code, response.Body.String())
	}
	if response := <-archive; response.Code != 303 {
		t.Fatalf("archive failed: %d %s", response.Code, response.Body.String())
	}
	expectStatus(t, router, cookie, "GET", path, 404)
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"Must not save after archive"}}, 404)
	assertContains(t, requestBody(t, router, adminCookie, "GET", path, 200), "Committed before archive")
}
