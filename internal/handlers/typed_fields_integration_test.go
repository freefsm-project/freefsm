package handlers

import (
	"context"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

func fieldPost(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values, want int) *httptest.ResponseRecorder {
	t.Helper()
	if want != http.StatusForbidden && want != http.StatusNotFound && (path == "/customers" || regexp.MustCompile(`^/customers/\d+$`).MatchString(path)) {
		values = typedCustomerFormValues(t, router, cookie, path, values)
	}
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("POST %s: got %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w
}

// Capture the same operation and revision controls a user receives before
// submitting. Call this before acquiring race-test locks, never after waiting.
func typedCustomerFormValues(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values) url.Values {
	t.Helper()
	formPath := path + "/edit"
	if path == "/customers" {
		formPath = path + "/new"
	}
	form := requestBody(t, router, cookie, "GET", formPath, 200)
	result := url.Values{}
	for key, entries := range values {
		result[key] = append([]string(nil), entries...)
	}
	for _, key := range []string{"operation_id", "revision"} {
		match := regexp.MustCompile(`name="` + key + `" value="([^"]*)"`).FindStringSubmatch(form)
		if len(match) != 2 || (match[1] == "" && (key == "operation_id" || path != "/customers")) {
			t.Fatalf("%s missing %s control", formPath, key)
		}
		if !result.Has(key) {
			result.Set(key, html.UnescapeString(match[1]))
		}
	}
	return result
}

func TestTypedFieldsConcurrentDefinitionAndAuditFailureHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Readout"}, "object_type": {"customer"}, "field_type": {"number"}, "precision": {"3"}}, 303)
	created := fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Atomic customer"}, "custom_field_1": {"1.23"}}, 303)
	path := strings.TrimSuffix(created.Header().Get("Location"), "/edit")
	var originalFields string
	if err := pool.QueryRow(ctx, `SELECT custom_fields FROM customers WHERE display_name='Atomic customer'`).Scan(&originalFields); err != nil {
		t.Fatal(err)
	}
	input := typedCustomerFormValues(t, router, cookie, path, url.Values{"display_name": {"Must roll back"}, "custom_field_1": {"1.234"}})
	writer, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback(ctx)
	if _, err = writer.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = writer.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", path, strings.NewReader(input.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("save did not reach authorization fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = writer.Exec(ctx, `UPDATE custom_field_definitions SET precision=2 WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if err = writer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if w := <-done; w.Code != 422 {
		t.Fatalf("stale precision save: %d %s", w.Code, w.Body.String())
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Atomic customer")
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_field_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER fail_field_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_field_audit()`); err != nil {
		t.Fatal(err)
	}
	t.Run("audit rejection status", func(t *testing.T) {
		fieldPost(t, router, cookie, path, url.Values{"display_name": {"Audit must roll back"}, "custom_field_1": {"2.00"}}, 409)
	})
	// Check atomicity even if the handler's error mapping regresses.
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Atomic customer")
	var fields string
	if err := pool.QueryRow(ctx, `SELECT custom_fields FROM customers WHERE display_name='Atomic customer'`).Scan(&fields); err != nil {
		t.Fatal(err)
	}
	if fields != originalFields {
		t.Fatalf("failed saves changed custom fields: got %s want %s", fields, originalFields)
	}
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Failed definition"}, "object_type": {"customer"}, "field_type": {"text"}}, 422)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/settings/custom-fields", 200), "Failed definition")
}

func TestTypedFieldsDefaultsNeverBackfillAndReferencesRemainHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	old := fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Old customer"}}, 303)
	oldPath := strings.TrimSuffix(old.Header().Get("Location"), "/edit")
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Region"}, "object_type": {"customer"}, "field_type": {"select"}, "options": {`["North","South"]`}, "default_value": {"North"}}, 303)
	assertNotContains(t, requestBody(t, router, cookie, "GET", oldPath, 200), ">North<")
	fieldPost(t, router, cookie, oldPath, url.Values{"display_name": {"Old edited"}}, 303)
	assertNotContains(t, requestBody(t, router, cookie, "GET", oldPath, 200), ">North<")
	fresh := fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"New customer"}}, 303)
	path := strings.TrimSuffix(fresh.Header().Get("Location"), "/edit")
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "North")
	fieldPost(t, router, cookie, "/settings/custom-fields/1/delete", url.Values{}, 422)
	fieldPost(t, router, cookie, "/settings/custom-fields/1", url.Values{"name": {"Region"}, "field_type": {"number"}, "precision": {"2"}}, 422)
	fieldPost(t, router, cookie, "/settings/custom-fields/1", url.Values{"name": {"Region"}, "field_type": {"select"}, "options": {`["South"]`}}, 422)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "North")
	fieldPost(t, router, cookie, "/settings/custom-fields/1", url.Values{"name": {"Region"}, "field_type": {"select"}, "options": {`["North","South"]`}, "default_value": {"South"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "North")
	form := requestBody(t, router, cookie, "GET", "/customers/new", 200)
	assertContains(t, form, `value="South" selected`)
}

func TestTypedFieldsJobPlanningDoesNotGrantValuesAndRestoreExemptionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Planner without fields", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.edit", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	planner := client.User.Create().SetCompanyID(company).SetEmail("planner@test").SetName("Planner").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, planner.ID, role); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Restore customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Planning job").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, planner.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, adminCookie, "/settings/custom-fields", url.Values{"name": {"Job flag"}, "object_type": {"job"}, "field_type": {"checkbox"}}, 303)
	input := url.Values{"job_type": {"Changed planning"}, "customer_id": {fmt.Sprint(customer.ID)}, "form_mode": {"edit"}, "job_id": {fmt.Sprint(job.ID)}, "custom_field_1": {"false"}}
	path := fmt.Sprintf("/jobs/%d", job.ID)
	fieldPost(t, router, cookie, path, input, 403)
	expectStatus(t, router, cookie, "GET", path+"/custom-fields", 403)
	input.Del("custom_field_1")
	fieldPost(t, router, cookie, path, input, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Changed planning")
	// Newly required Customer fields do not block independent restore.
	client.Customer.UpdateOneID(customer.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	fieldPost(t, router, adminCookie, "/settings/custom-fields", url.Values{"name": {"New required"}, "object_type": {"customer"}, "field_type": {"text"}, "required": {"true"}}, 303)
	customerPath := fmt.Sprintf("/customers/%d", customer.ID)
	fieldPost(t, router, adminCookie, customerPath+"/restore", url.Values{}, 303)
	assertContains(t, requestBody(t, router, adminCookie, "GET", customerPath, 200), "Restore customer")
	fieldPost(t, router, adminCookie, customerPath, url.Values{"display_name": {"Must supply new field"}}, 422)
}

func TestTypedFieldsRecoveryReversionDefersNewRequiredFieldHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recovery customer").SaveX(ctx)
	fieldPost(t, router, cookie, "/estimates", url.Values{"title": {"Recoverable estimate"}, "customer_id": {fmt.Sprint(customer.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	list := requestBody(t, router, cookie, "GET", "/estimates", 200)
	match := regexp.MustCompile(`href="(/estimates/[0-9]+)"`).FindStringSubmatch(list)
	if len(match) != 2 {
		t.Fatal("missing estimate link")
	}
	path := match[1]
	converted := fieldPost(t, router, cookie, path+"/convert", url.Values{"idempotency_key": {"29111111-1111-4111-8111-111111111111"}}, 303)
	invoicePath := strings.Split(converted.Header().Get("Location"), "?")[0]
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Required after conversion"}, "object_type": {"estimate"}, "field_type": {"checkbox"}, "required": {"true"}}, 303)
	fieldPost(t, router, cookie, invoicePath+"/revert-to-estimate", url.Values{"idempotency_key": {"29222222-2222-4222-8222-222222222222"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Recoverable estimate")
	fieldPost(t, router, cookie, path, url.Values{"title": {"Rejected details"}, "customer_id": {fmt.Sprint(customer.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}}, 422)
}

func TestTypedParentEditAloneAllowsValuesHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Restricted master").SaveX(ctx)
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName("Project values").SaveX(ctx)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Project-only editor", []authorization.Grant{{Permission: "project.view", Scope: authorization.All}, {Permission: "project.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("projectonly@test").SetName("Project editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	fieldPost(t, router, adminCookie, "/settings/custom-fields", url.Values{"name": {"Project memo"}, "object_type": {"project"}, "field_type": {"textarea"}}, 303)
	path := fmt.Sprintf("/projects/%d/custom-fields", project.ID)
	expectStatus(t, router, cookie, "GET", path, 200)
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"First line\nSecond line"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Second line")
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", customer.ID), 404)
	expectStatus(t, router, cookie, "GET", "/settings/custom-fields", 403)
}

func TestTypedAssignedJobSaveHoldsAssignmentUntilCommitHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	user := client.User.Create().SetCompanyID(company).SetEmail("assignment-race@test").SetName("Assigned editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, 3); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Assignment race").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(user.ID).SaveX(ctx)
	if _, err := auth.SaveField(ctx, authorization.Actor{UserID: admin}, 0, authorization.FieldInput{ObjectType: "job", Name: "Race field", Type: "text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION hold_field_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='job.fields.edit' THEN PERFORM pg_advisory_xact_lock(190029); END IF; RETURN NEW; END $$; CREATE TRIGGER hold_field_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION hold_field_audit()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(190029)`); err != nil {
		t.Fatal(err)
	}
	var blockerPID int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, user.ID, "Assignment race")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("PATCH", fmt.Sprintf("/jobs/%d/custom-fields", job.ID), strings.NewReader(`{"values":[{"definition_id":1,"value":"Accepted before removal"}]}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		done <- w
	}()
	deadline := time.Now().Add(10 * time.Second)
	savePID := 0
	for savePID == 0 {
		if err := pool.QueryRow(ctx, `SELECT coalesce((SELECT pid FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)) LIMIT 1),0)`, blockerPID).Scan(&savePID); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("save did not reach audit barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	removed := make(chan error, 1)
	go func() {
		_, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, user.ID)
		removed <- err
	}()
	for {
		select {
		case err := <-removed:
			t.Fatalf("assignment removal crossed an uncommitted field save: %v", err)
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
			t.Fatal("assignment writer did not reach save lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if response := <-done; response.Code != 200 {
		t.Fatalf("save status %d: %s", response.Code, response.Body.String())
	}
	if err = <-removed; err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/custom-fields", job.ID), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatalf("removed assignment retained access: %d", w.Code)
	}
}

func TestTypedInvoiceFieldsRespectSettlementAndSeparateActionsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Settlement customer").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Invoice reference"}, "object_type": {"invoice"}, "field_type": {"text"}}, 303)
	input := url.Values{"title": {"Settlement invoice"}, "customer_id": {fmt.Sprint(customer.ID)}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100,"taxable":false}]`}, "tax_rate": {"0"}, "invoice_date": {"2026-10-11"}, "due_date": {"2026-11-11"}, "custom_field_1": {"Original reference"}}
	created := fieldPost(t, router, cookie, "/invoices", input, 303)
	path := strings.Split(created.Header().Get("Location"), "?")[0]
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Added after invoice"}, "object_type": {"invoice"}, "field_type": {"text"}, "required": {"true"}}, 303)
	// Finalization and settlement are separate actions: missing new fields defer.
	fieldPost(t, router, cookie, path+"/finalize", url.Values{}, 303)
	edit := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	match := regexp.MustCompile(`name="invoice_number"[^>]*value="([0-9]+)"`).FindStringSubmatch(edit)
	if len(match) != 2 {
		t.Fatal("missing invoice number")
	}
	input.Set("invoice_number", match[1])
	input.Set("custom_field_1", "Blocked field")
	input.Set("custom_field_2", "Supplied required")
	input.Set("title", "Blocked title")
	// Put settlement first in the Invoice's row-lock queue. A details request
	// that has already passed request authorization must recheck after it waits.
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `SELECT id FROM invoices WHERE title='Settlement invoice' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	async := func(target string, values url.Values) <-chan *httptest.ResponseRecorder {
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
	wait := func(want int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			var n int
			if err := pool.QueryRow(ctx, `WITH RECURSIVE blocked(pid) AS (SELECT $1::integer UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))) SELECT count(*)-1 FROM blocked`, pid).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n >= want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("expected %d blocked requests, got %d", want, n)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	payment := async(path+"/payments", url.Values{"amount": {"10.00"}, "method": {"cash"}, "date": {time.Now().Format("2006-01-02")}, "idempotency_key": {"typed-fields-payment"}})
	wait(1)
	save := async(path, input)
	wait(2)
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-payment; got.Code != 303 {
		t.Fatalf("settlement failed: %d %s", got.Code, got.Body.String())
	}
	failed := <-save
	if failed.Code != 422 {
		t.Fatalf("racing field save: %d %s", failed.Code, failed.Body.String())
	}
	assertContains(t, failed.Body.String(), "locked by active settlement")
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Original reference")
	assertContains(t, body, "Settlement invoice")
	assertNotContains(t, body, "Blocked title")
	fieldPost(t, router, cookie, path+"/comments", url.Values{"content": {"Settlement-locked collaboration remains available"}}, 200)
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/comments", 200), "Settlement-locked collaboration remains available")
}

func TestTypedJobFieldsIndependentGrantWebAndAPI(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Field only", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.fields.edit", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("fieldonly@test").SetName("Field only").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Field job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(user.ID).SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, adminCookie, "/settings/custom-fields", url.Values{"name": {"Inspection passed"}, "object_type": {"job"}, "field_type": {"checkbox"}, "required": {"true"}}, 303)
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	path := fmt.Sprintf("/jobs/%d/custom-fields", job.ID)
	expectStatus(t, router, cookie, "GET", path, 200)
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"false"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), `value="false" selected`)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, user.ID, "Fields")
	if err != nil {
		t.Fatal(err)
	}
	send := func(body string, want int) {
		t.Helper()
		r := httptest.NewRequest("PATCH", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("API got %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	send(`{"values":[{"definition_id":1,"value":"true"}]}`, 200)
	send(`{"values":[{"definition_id":1,"value":"maybe"}]}`, 422)
	fieldPost(t, router, adminCookie, "/settings/custom-fields", url.Values{"name": {"Later required"}, "object_type": {"job"}, "field_type": {"text"}, "required": {"true"}}, 303)
	// Atomic field patches validate changed values; a later unrelated required
	// definition is deferred to the next online main-details save.
	send(`{"values":[{"definition_id":1,"value":"true"}]}`, 200)
	send(`{"values":[{"definition_id":2,"value":""}]}`, 422)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), `value="true" selected`)
	client.JobAssignment.Delete().ExecX(ctx)
	send(`{"values":[{"definition_id":1,"value":"false"}]}`, 404)
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"false"}}, 404)
}

func TestTypedFieldDatesOptionsAndShapesHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	for _, v := range []url.Values{
		{"name": {"Inspection"}, "object_type": {"customer"}, "field_type": {"date"}, "options": {"[]"}},
		{"name": {"Equipment"}, "object_type": {"customer"}, "field_type": {"multiselect"}, "options": {`["Pump","Motor"]`}},
	} {
		fieldPost(t, router, cookie, "/settings/custom-fields", v, 303)
	}
	created := fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Typed customer"}, "custom_field_1": {"2024-02-29"}, "custom_field_2": {"Pump", "Motor"}}, 303)
	path := strings.TrimSuffix(created.Header().Get("Location"), "/edit")
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Pump")
	assertContains(t, body, "Motor")
	for _, v := range []url.Values{
		{"custom_field_1": {"2023-02-29"}},
		{"custom_field_1": {"2024-02-29", "2024-03-01"}},
		{"custom_field_2": {"Unknown"}},
		{"custom_field_bogus": {"smuggled"}},
		{"custom_field_999": {"smuggled"}},
	} {
		fieldPost(t, router, cookie, path, v, 422)
	}
	for _, parent := range []string{"contact", "location", "item", "time_entry"} {
		fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Unsupported"}, "object_type": {parent}, "field_type": {"text"}, "options": {"[]"}}, 422)
	}
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Bad options"}, "object_type": {"customer"}, "field_type": {"select"}, "options": {`["Pump"," pump "]`}}, 422)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Unicode collision"}, "object_type": {"customer"}, "field_type": {"select"}, "options": {`["Σ"," ς "]`}}, 422)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Ambiguous default"}, "object_type": {"customer"}, "field_type": {"multiselect"}, "options": {`["Pump"]`}, "default_value": {"null"}}, 422)
	_ = fmt.Sprint(path)
}

func TestTypedFieldsExistingParentDetailsAtomicHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Parent customer").SaveX(ctx)
	for _, typ := range []string{"project", "job", "asset", "estimate", "invoice"} {
		fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {typ + " inspection"}, "object_type": {typ}, "field_type": {"date"}, "required": {"true"}}, 303)
	}
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName("Original project").SaveX(ctx)
	// Definitions added after parent creation are enforced even without field keys.
	fieldPost(t, router, cookie, fmt.Sprintf("/projects/%d", project.ID), url.Values{"name": {"Rejected project"}, "customer_id": {fmt.Sprint(customer.ID)}, "completion_percentage": {"0"}}, 422)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/projects/%d", project.ID), 200), "Original project")
	fieldPost(t, router, cookie, fmt.Sprintf("/projects/%d", project.ID), url.Values{"name": {"Saved project"}, "customer_id": {fmt.Sprint(customer.ID)}, "completion_percentage": {"0"}, "custom_field_1": {"2024-02-29"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/projects/%d", project.ID), 200), "Saved project")
	assetType := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	for _, tc := range []struct {
		path   string
		fields url.Values
		key    string
	}{
		{"/assets", url.Values{"name": {"Saved asset"}, "customer_id": {fmt.Sprint(customer.ID)}, "asset_type_id": {fmt.Sprint(assetType.ID)}}, "custom_field_3"},
		{"/estimates", url.Values{"title": {"Saved estimate"}, "customer_id": {fmt.Sprint(customer.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}}, "custom_field_4"},
		{"/invoices", url.Values{"title": {"Saved invoice"}, "customer_id": {fmt.Sprint(customer.ID)}, "line_items": {"[]"}, "tax_rate": {"0"}, "invoice_date": {"2026-10-11"}, "due_date": {"2026-11-11"}}, "custom_field_5"},
	} {
		fieldPost(t, router, cookie, tc.path, tc.fields, 422)
		tc.fields.Set(tc.key, "2024-02-29")
		saved := fieldPost(t, router, cookie, tc.path, tc.fields, 303)
		t.Logf("saved %s redirects %s", tc.path, saved.Header().Get("Location"))
		list := requestBody(t, router, cookie, "GET", tc.path, 200)
		match := regexp.MustCompile(`href="(` + tc.path + `/[0-9]+)"`).FindStringSubmatch(list)
		if len(match) != 2 {
			t.Fatalf("no record link on %s", tc.path)
		}
		target := match[1]
		assertContains(t, requestBody(t, router, cookie, "GET", target, 200), "inspection")
	}
	// Minimal Job creation is the explicit requiredness exception.
	created := fieldPost(t, router, cookie, "/jobs", url.Values{"job_type": {"Minimal Job"}, "customer_id": {fmt.Sprint(customer.ID)}, "form_mode": {"create"}, "job_id": {"0"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", created.Header().Get("Location"), 200), "Minimal Job")
	jobList := requestBody(t, router, cookie, "GET", "/jobs", 200)
	jobLink := regexp.MustCompile(`href="(/jobs/[0-9]+)"`).FindStringSubmatch(jobList)
	if len(jobLink) != 2 {
		t.Fatal("missing Job link")
	}
	jobPath := jobLink[1]
	fieldPost(t, router, cookie, jobPath+"/create-estimate", url.Values{}, 422)
	fieldPost(t, router, cookie, jobPath+"/create-estimate", url.Values{"custom_field_4": {"2024-02-29"}}, 303)
	inlineProject := url.Values{"name": {"Inline project"}, "customer_id": {fmt.Sprint(customer.ID)}}
	fieldPost(t, router, cookie, "/projects/inline", inlineProject, 422)
	inlineProject.Set("custom_field_1", "2024-02-29")
	fieldPost(t, router, cookie, "/projects/inline", inlineProject, 200)
	inlineAsset := url.Values{"name": {"Inline asset"}, "customer_id": {fmt.Sprint(customer.ID)}, "asset_type_id": {fmt.Sprint(assetType.ID)}}
	fieldPost(t, router, cookie, "/assets/inline", inlineAsset, 422)
	inlineAsset.Set("custom_field_3", "2024-02-29")
	fieldPost(t, router, cookie, "/assets/inline", inlineAsset, 200)
}

func TestTypedFieldRequiredCheckboxAtomicHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Safety reviewed"}, "object_type": {"customer"}, "field_type": {"checkbox"}, "required": {"true"}, "options": {"[]"}}, 303)
	// A missing checkbox is unknown, not No. A rejected save creates no parent.
	failed := fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Entered customer"}}, 422)
	assertContains(t, failed.Body.String(), "Safety reviewed")
	assertContains(t, failed.Body.String(), "Entered customer")
	list := requestBody(t, router, cookie, "GET", "/customers", 200)
	assertNotContains(t, list, "Entered customer")
	form := requestBody(t, router, cookie, "GET", "/customers/new", 200)
	assertContains(t, form, `value="false"`)
	// The first identity is discovered through the rendered form, not assumed.
	start := strings.Index(form, `name="custom_field_`)
	if start < 0 {
		t.Fatal("missing field input")
	}
	key := strings.SplitN(form[start+6:], `"`, 2)[0]
	created := fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Entered customer"}, key: {"false"}}, 303)
	path := strings.TrimSuffix(created.Header().Get("Location"), "/edit")
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Safety reviewed")
	assertContains(t, body, "No")
	fieldPost(t, router, cookie, path, url.Values{"display_name": {"Should roll back"}, key: {"maybe"}}, 422)
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Entered customer")
	assertNotContains(t, body, "Should roll back")
}

func TestTypedFieldDefinitionGrantsPrecisionDefaultsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Field designer", []authorization.Grant{{Permission: "configuration.fields.view", Scope: authorization.Configuration}, {Permission: "configuration.fields.manage", Scope: authorization.Configuration}})
	if err != nil {
		t.Fatal(err)
	}
	designer := client.User.Create().SetCompanyID(company).SetEmail("designer@test").SetName("Designer").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, designer.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, designer.ID)
	expectStatus(t, router, cookie, "GET", "/settings/custom-fields", 200)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {" Rated power "}, "object_type": {"customer"}, "field_type": {"number"}, "precision": {"2"}, "default_value": {"12.30"}, "options": {"[]"}}, 303)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"rated POWER"}, "object_type": {"customer"}, "field_type": {"text"}, "options": {"[]"}}, 422)
	fieldPost(t, router, cookie, "/customers", url.Values{"display_name": {"Unauthorized"}}, 403)
	if _, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Field viewer", []authorization.Grant{{Permission: "configuration.fields.view", Scope: authorization.Configuration}}, 1); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", "/settings/custom-fields", 200)
	expectStatus(t, router, cookie, "GET", "/settings/custom-fields/new", 403)
	fieldPost(t, router, cookie, "/settings/custom-fields/1", url.Values{"name": {"Denied"}, "field_type": {"text"}}, 403)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	created := fieldPost(t, router, adminCookie, "/customers", url.Values{"display_name": {"Defaulted customer"}}, 303)
	path := strings.TrimSuffix(created.Header().Get("Location"), "/edit")
	body := requestBody(t, router, adminCookie, "GET", path, 200)
	assertContains(t, body, "12.30")
	expectStatus(t, router, cookie, "GET", path, 404)
	editorRole, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Record editor only", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, designer.ID, editorRole); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", "/settings/custom-fields", 403)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "12.30")
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"10.25"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "10.25")
	form := requestBody(t, router, adminCookie, "GET", path+"/edit", 200)
	start := strings.Index(form, `name="custom_field_`)
	key := strings.SplitN(form[start+6:], `"`, 2)[0]
	fieldPost(t, router, adminCookie, path, url.Values{"display_name": {"Should roll back"}, key: {"12.345"}}, 422)
	assertContains(t, requestBody(t, router, adminCookie, "GET", path, 200), "Defaulted customer")
	fieldPost(t, router, adminCookie, path, url.Values{key: {"0.01"}}, 303)
	assertContains(t, requestBody(t, router, adminCookie, "GET", path, 200), "0.01")
}
