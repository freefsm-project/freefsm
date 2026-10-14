package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"golang.org/x/net/html"
)

func TestGuardedPreferencesDurableIdentityHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/preferences", 200)
	assertContains(t, body, `name="save_action_id"`)
	op := newOperationKey()
	post := func(theme string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/preferences", strings.NewReader(url.Values{"theme": {theme}, "font_size": {"large"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-FreeFSM-Action", op)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := post("dark")
	if w.Code != http.StatusSeeOther || w.Header().Get("X-FreeFSM-Disposition") != "saved" {
		t.Fatalf("save: %d %v %s", w.Code, w.Header(), w.Body.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal([]byte(requestBody(t, router, cookie, "GET", "/save-actions/"+op, 200)), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["disposition"] != "saved" || receipt["committed_steps"] != float64(1) {
		t.Fatalf("receipt: %v", receipt)
	}
	if w = post("dark"); w.Code != 409 {
		t.Fatalf("duplicate dispatched: %d %s", w.Code, w.Body.String())
	}
	if w = post("light"); w.Code != 409 || !strings.Contains(w.Body.String(), "identity_conflict") {
		t.Fatalf("changed intent: %d %s", w.Code, w.Body.String())
	}
	assertContains(t, requestBody(t, router, cookie, "GET", "/preferences", 200), `data-theme="dark"`)
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+op+"/review", 200), "Review current state")
}

func TestGuardedStructuredEstimateConflictRetainsProposedLinesHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Structured parent").SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Structured estimate"}, "tax_rate": {"0"}, "line_items": {`[{"title":"Original line","quantity":1,"unit_price":10,"description":"Original description"}]`}}
	itemPost(t, router, cookie, "/estimates", values, 303)
	id := client.Estimate.Query().OnlyX(ctx).ID
	path := fmt.Sprintf("/estimates/%d", id)
	form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	revision := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(form)
	if len(revision) != 2 {
		t.Fatal("missing revision")
	}
	values.Set("line_items", `[{"title":"Current line","quantity":2,"unit_price":10,"description":"Current authorized description"}]`)
	itemPost(t, router, cookie, path, values, 303)
	values.Set("save_action_id", newOperationKey())
	values.Set("save_revision", revision[1])
	values.Set("line_items", `[{"title":"Proposed line","quantity":3.5,"unit_price":12.5,"description":"Retain proposed </script> description"}]`)
	response := itemPost(t, router, cookie, path, values, 409)
	assertContains(t, response, "Current authorized description")
	doc, err := html.Parse(strings.NewReader(response))
	if err != nil {
		t.Fatal(err)
	}
	var proposed string
	walkSaveNodes(doc, func(n *html.Node) {
		if saveAttr(n, "data-line-item-json") == "existing" {
			proposed = saveNodeText(n)
		}
	})
	var lines []struct {
		Title, Description string
		Quantity           float64
	}
	if err := json.Unmarshal([]byte(proposed), &lines); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || lines[0].Title != "Proposed line" || lines[0].Quantity != 3.5 || lines[0].Description != "Retain proposed </script> description" {
		t.Fatalf("lost structured input: %s", proposed)
	}
	assertNotContains(t, response, `Retain proposed </script>`)
	review := regexp.MustCompile(`name="save_conflict_review" value="([^"]+)"`).FindStringSubmatch(response)
	if len(review) != 2 {
		t.Fatal("missing reviewed submitter")
	}
	values.Set("save_action_id", newOperationKey())
	values.Set("save_conflict_review", review[1])
	itemPost(t, router, cookie, path, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Proposed line")
}

func TestGuardedStructuredJobConflictRetainsVisitsAndSubtasksHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Structured Job parent").SaveX(ctx)
	values := url.Values{"form_mode": {"create"}, "job_id": {"0"}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Structured Job"}, "visits": {"[]"}, "subtasks": {"[]"}}
	itemPost(t, router, cookie, "/jobs", values, 303)
	id := client.Job.Query().OnlyX(ctx).ID
	path := fmt.Sprintf("/jobs/%d", id)
	form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	revision := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(form)
	if len(revision) != 2 {
		t.Fatal("missing Job revision")
	}
	values.Set("form_mode", "edit")
	values.Set("job_id", fmt.Sprint(id))
	values.Set("job_type", "Current Job")
	values.Set("subtasks", `[{"title":"Current task","completed":false,"sort_order":1}]`)
	itemPost(t, router, cookie, path, values, 303)
	values.Set("save_action_id", newOperationKey())
	values.Set("save_revision", revision[1])
	values.Set("subtasks", `[{"title":"Proposed task","completed":false,"sort_order":2}]`)
	values.Set("visits", `[{"date":"2026-11-01","start_time":"09:15","end_time":"11:45","notes":"Proposed visit notes"}]`)
	body := itemPost(t, router, cookie, path, values, 409)
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	bootstraps := map[string]string{}
	walkSaveNodes(doc, func(n *html.Node) {
		if key := saveAttr(n, "data-save-retained-structured"); key != "" {
			bootstraps[key] = saveAttr(n, "data-existing")
		}
	})
	if !strings.Contains(bootstraps["visits"], "Proposed visit notes") || !strings.Contains(bootstraps["subtasks"], "Proposed task") {
		t.Fatalf("lost Job widgets %v", bootstraps)
	}
	assertContains(t, body, "Current task")
}

func TestGuardedKnownLookupAndSameStatusCompleteWithoutInventedWriteHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Read-only parent").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Lookup address").SetAddress1("123 Main Street").SaveX(ctx)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"lat":"12.34","lon":"56.78"}]`))
	}))
	defer provider.Close()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET geocoder_url=$1 WHERE company_id=$2`, provider.URL, company); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/locations/%d/coordinates", loc.ID)
	page := requestBody(t, router, cookie, "GET", path, 200)
	revision := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(page)
	if len(revision) != 2 {
		t.Fatal("missing coordinate revision")
	}
	key := newOperationKey()
	proposal := itemPost(t, router, cookie, path+"/lookup", url.Values{"save_action_id": {key}, "revision": {revision[1]}}, 200)
	assertContains(t, proposal, "12.34")
	assertNotContains(t, requestBody(t, router, cookie, "GET", path, 200), `value="12.34"`)
	receipt := requestBody(t, router, cookie, "GET", "/save-actions/"+key, 200)
	assertContains(t, receipt, `"disposition":"completed"`)
	assertContains(t, receipt, `"committed_steps":0`)
	parentRevision := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(page)
	if len(parentRevision) != 2 {
		t.Fatal("missing coordinate parent revision")
	}
	coordinates := url.Values{"save_action_id": {newOperationKey()}, "save_revision": {parentRevision[1]}, "revision": {revision[1]}, "latitude": {"1"}, "longitude": {"2"}}
	itemPost(t, router, cookie, path, coordinates, 303)
	coordinates.Set("save_action_id", newOperationKey())
	coordinates.Set("latitude", "3")
	conflict := itemPost(t, router, cookie, path, coordinates, 409)
	reviewed := regexp.MustCompile(`name="save_conflict_review" value="([^"]+)"`).FindStringSubmatch(conflict)
	if len(reviewed) != 2 {
		t.Fatal("missing combined coordinate revision review")
	}
	coordinates.Set("save_action_id", newOperationKey())
	coordinates.Set("save_conflict_review", reviewed[1])
	itemPost(t, router, cookie, path, coordinates, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), `value="3"`)
	itemPost(t, router, cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Status no-op"}, "tax_rate": {"0"}, "line_items": {"[]"}}, 303)
	estimate := client.Estimate.Query().OnlyX(ctx)
	key = newOperationKey()
	itemPost(t, router, cookie, fmt.Sprintf("/estimates/%d/status", estimate.ID), url.Values{"save_action_id": {key}, "status_id": {fmt.Sprint(*estimate.StatusID)}}, 303)
	receipt = requestBody(t, router, cookie, "GET", "/save-actions/"+key, 200)
	assertContains(t, receipt, `"disposition":"completed"`)
	assertContains(t, receipt, `"committed_steps":0`)
}

func TestGuardedUnknownReceiptRevalidatesLostCostFacetHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Facet editor", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "item.edit", Scope: authorization.All}, {Permission: "cost.view", Scope: authorization.All}, {Permission: "cost.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Facet owner").SetEmail("facet-unknown@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	item := client.Item.Create().SetCompanyID(company).SetName("Visible base").SetUnitCost(987.65).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/items/%d", item.ID)
	id := newOperationKey()
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_unknown_item_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='item' THEN RAISE EXCEPTION 'audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_unknown_item_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_unknown_item_audit(); CREATE FUNCTION fail_unknown_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'completion failure'; END $$; CREATE TRIGGER fail_unknown_completion BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION fail_unknown_completion()`); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, path, url.Values{"save_action_id": {id}, "unit_cost": {"543.21"}}, 409)
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+id, 200), `"disposition":"unknown"`)
	before := requestBody(t, router, cookie, "GET", "/save-actions/"+id+"/input-access", 200)
	assertContains(t, before, "unit_cost")
	assertNotContains(t, before, "987.65")
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Base editor only", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "item.edit", Scope: authorization.All}}, 1); err != nil {
		t.Fatal(err)
	}
	after := requestBody(t, router, cookie, "GET", "/save-actions/"+id+"/input-access", 200)
	assertNotContains(t, after, "unit_cost")
	assertNotContains(t, after, "543.21")
	assertContains(t, after, `"name"`)
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+id, 200), `"disposition":"unknown"`)
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "No Item access", nil, 2); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", "/save-actions/"+id+"/input-access", 403)
}

func TestGuardedLookupRechecksEditGrantAfterProviderIOHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Coordinate editor", []authorization.Grant{{Permission: "location.view", Scope: authorization.All}, {Permission: "location.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Coordinate owner").SetEmail("coordinate-race@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Coordinate parent").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Address").SetAddress1("123 Main Street").SaveX(ctx)
	entered, release := make(chan struct{}), make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		_, _ = w.Write([]byte(`[{"lat":"12.34","lon":"56.78"}]`))
	}))
	defer provider.Close()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET geocoder_url=$1 WHERE company_id=$2`, provider.URL, company); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/locations/%d/coordinates", loc.ID)
	page := requestBody(t, router, cookie, "GET", path, 200)
	rev := regexp.MustCompile(`name="revision" value="([^"]+)"`).FindStringSubmatch(page)
	if len(rev) != 2 {
		t.Fatal("missing revision")
	}
	key := newOperationKey()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", path+"/lookup", strings.NewReader(url.Values{"save_action_id": {key}, "revision": {rev[1]}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w
	}()
	<-entered
	_, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Coordinate viewer", []authorization.Grant{{Permission: "location.view", Scope: authorization.All}}, 1)
	if err == nil {
		_, err = pool.Exec(ctx, `UPDATE locations SET address_1='Changed during lookup' WHERE id=$1`, loc.ID)
	}
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != 403 {
		t.Fatalf("revoked lookup: %d", w.Code)
	}
	assertNotContains(t, w.Body.String(), "12.34")
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+key, 200), `"disposition":"rejected"`)
}

func TestGuardedJobInvoicePreparationIsReadOnlyAndCreationAtomicHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Document source parent").SaveX(ctx)
	itemPost(t, router, cookie, "/jobs", url.Values{"form_mode": {"create"}, "job_id": {"0"}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Invoice source"}}, 303)
	job := client.Job.Query().OnlyX(ctx)
	base := fmt.Sprintf("/jobs/%d", job.ID)
	prepare := newOperationKey()
	body := itemPost(t, router, cookie, base+"/create-invoice", url.Values{"save_action_id": {prepare}}, 200)
	assertContains(t, body, `name="line_items"`)
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+prepare, 200), `"disposition":"completed"`)
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+prepare, 200), `"location":"`+base+`/create-invoice"`)
	form := requestBody(t, router, cookie, "GET", base+"/create-invoice", 200)
	revision := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(form)
	if len(revision) != 2 {
		t.Fatal("missing Job source revision")
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION interrupt_job_document_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.completed_at IS NOT NULL THEN RAISE EXCEPTION 'completion interrupted'; END IF; RETURN NEW; END $$; CREATE TRIGGER interrupt_job_document_completion BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION interrupt_job_document_completion()`); err != nil {
		t.Fatal(err)
	}
	created := newOperationKey()
	itemPost(t, router, cookie, base+"/invoices", url.Values{"save_action_id": {created}, "save_revision": {revision[1]}, "customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Actual guarded Invoice"}, "tax_rate": {"0"}, "line_items": {"[]"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+created, 200), `"disposition":"saved"`)
	if _, err := pool.Exec(ctx, `DROP TRIGGER interrupt_job_document_completion ON online_save_actions`); err != nil {
		t.Fatal(err)
	}
	invoiceID := client.Invoice.Query().OnlyX(ctx).ID
	rejected := newOperationKey()
	itemPost(t, router, cookie, fmt.Sprintf("/invoices/%d/email", invoiceID), url.Values{"save_action_id": {rejected}}, 409)
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+rejected, 200), `"disposition":"rejected"`)
	mailRejected := newOperationKey()
	itemPost(t, router, cookie, "/settings/test-email", url.Values{"save_action_id": {mailRejected}, "recipient": {"not-an-email"}}, 400)
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+mailRejected, 200), `"disposition":"rejected"`)
}

func TestGuardedInstalledParentFormsAndActionsHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Guarded parents").SaveX(ctx)
	settings := requestBody(t, router, cookie, "GET", "/settings", 200)
	for _, tc := range []struct {
		name, path string
		values     url.Values
	}{
		{"configuration", "/settings", url.Values{"section": {"company"}, "revision": {configurationRevision(t, settings)}, "business_name": {"Guarded company"}}},
		{"Item", "/items", url.Values{"name": {"Guarded Item"}, "type": {"service"}}},
		{"Contact", fmt.Sprintf("/customers/%d/contacts", customer.ID), url.Values{"first_name": {"Guarded"}, "last_name": {"Contact"}}},
		{"Location", fmt.Sprintf("/customers/%d/locations", customer.ID), url.Values{"title": {"Guarded location"}}},
		{"Job", "/jobs", url.Values{"form_mode": {"create"}, "job_id": {"0"}, "customer_id": {fmt.Sprint(customer.ID)}, "job_type": {"Guarded job"}}},
		{"Estimate", "/estimates", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Guarded estimate"}, "line_items": {"[]"}, "tax_rate": {"0"}}},
		{"Invoice", "/invoices", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Guarded invoice"}, "line_items": {"[]"}, "tax_rate": {"0"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := newOperationKey()
			tc.values.Set("save_action_id", id)
			post := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.values.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				return w
			}
			if w := post(); w.Code != 303 || w.Header().Get("X-FreeFSM-Disposition") != "saved" {
				t.Fatalf("save %d %v %s", w.Code, w.Header(), w.Body.String())
			}
			receipt := requestBody(t, router, cookie, "GET", "/save-actions/"+id, 200)
			assertContains(t, receipt, `"disposition":"saved"`)
			assertContains(t, receipt, `"target_id":`)
			if w := post(); w.Code != 409 {
				t.Fatalf("duplicate %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestGuardedReceiptSurvivesGrantLossWithoutPrivateReplayHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Guarded Item editor", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "item.create", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Receipt owner").SetEmail("guarded@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	id := newOperationKey()
	values := url.Values{"name": {"PRIVATE-INPUT"}, "type": {"service"}, "save_action_id": {id}}
	itemPost(t, router, cookie, "/items", values, 303)
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "No catalog", nil, 1); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/save-actions/"+id, 200)
	assertContains(t, body, `"disposition":"saved"`)
	assertNotContains(t, body, "PRIVATE-INPUT")
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+id+"/review", 200), "PRIVATE-INPUT")
	itemPost(t, router, cookie, "/items", values, 409)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	// A missing own receipt without origin-history proof cannot establish that
	// an action was never received. This also discloses no other actor's receipt.
	body = requestBody(t, router, adminCookie, "GET", "/save-actions/"+id, 200)
	assertContains(t, body, `"disposition":"review"`)
	assertContains(t, body, `"history_changed":true`)
	assertNotContains(t, body, "PRIVATE-INPUT")
}

func TestGuardedSaveLostCompletionNeverRedispatchesAfterRestartHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION interrupt_online_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.completed_at IS NOT NULL THEN RAISE EXCEPTION 'simulated process interruption before completion'; END IF; RETURN NEW; END $$; CREATE TRIGGER interrupt_online_completion BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION interrupt_online_completion()`); err != nil {
		t.Fatal(err)
	}
	id := newOperationKey()
	post := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/dashboard/widgets/reset", strings.NewReader(""))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("X-FreeFSM-Action", id)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := post(); w.Code != 303 || w.Header().Get("X-FreeFSM-Disposition") != "unknown" {
		t.Fatalf("interrupted completion: %d %v", w.Code, w.Header())
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER interrupt_online_completion ON online_save_actions`); err != nil {
		t.Fatal(err)
	}
	router = New(pool, client, sessions, &config.Config{UploadDir: t.TempDir(), MaxUploadSize: 25 << 20})
	body := requestBody(t, router, cookie, "GET", "/save-actions/"+id, 200)
	assertContains(t, body, `"disposition":"unknown"`)
	assertContains(t, body, `"committed_steps":0`)
	if w := post(); w.Code != 409 {
		t.Fatalf("unknown re-dispatched: %d %s", w.Code, w.Body.String())
	}
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+id, 200), `"committed_steps":0`)
}

func TestGuardedTransactionalReceiptSurvivesLostCompletionHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION interrupt_atomic_completion() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.completed_at IS NOT NULL THEN RAISE EXCEPTION 'completion interrupted'; END IF; RETURN NEW; END $$; CREATE TRIGGER interrupt_atomic_completion BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION interrupt_atomic_completion()`); err != nil {
		t.Fatal(err)
	}
	id := newOperationKey()
	server := httptest.NewServer(router)
	defer server.Close()
	r, err := http.NewRequest("POST", server.URL+"/preferences", strings.NewReader("theme=dark&font_size=large"))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-FreeFSM-Action", id)
	r.AddCookie(cookie)
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Request.Method != "GET" {
		t.Fatalf("followed redirect: %d %s", response.StatusCode, response.Request.Method)
	}
	if response.Header.Get("X-FreeFSM-Disposition") != "" {
		t.Fatal("final GET must not pretend to carry POST confirmation")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+id, 200), `"disposition":"saved"`)
	assertContains(t, requestBody(t, router, cookie, "GET", "/preferences", 200), `data-theme="dark"`)
}

func TestGuardedPreferencesRejectStaleFormHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/preferences", 200)
	revisions := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(revisions) != 1 {
		t.Fatalf("expected preference revision, found %d", len(revisions))
	}
	post := func(theme string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/preferences", strings.NewReader(url.Values{"save_action_id": {newOperationKey()}, "save_revision": {revisions[0][1]}, "theme": {theme}, "font_size": {"large"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := post("dark"); w.Code != 303 {
		t.Fatalf("first edit %d %s", w.Code, w.Body.String())
	}
	if w := post("light"); w.Code != 409 {
		t.Fatalf("stale edit %d %s", w.Code, w.Body.String())
	}
	assertContains(t, requestBody(t, router, cookie, "GET", "/preferences", 200), `data-theme="dark"`)
}

func TestGuardedItemConflictRetainsOnlyAuthorizedInputsHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	item := client.Item.Create().SetCompanyID(company).SetName("Original").SaveX(ctx)
	path := fmt.Sprintf("/items/%d", item.ID)
	body := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	revision := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(body)
	if len(revision) != 2 {
		t.Fatal("missing Item revision")
	}
	itemPost(t, router, cookie, path, url.Values{"name": {"Current authorized value"}}, 303)
	values := url.Values{"save_action_id": {newOperationKey()}, "save_revision": {revision[1]}, "name": {"My retained correction"}, "type": {"service"}}
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("conflict %d %s", w.Code, w.Body.String())
	}
	assertContains(t, w.Body.String(), "Current authorized value")
	assertContains(t, w.Body.String(), "My retained correction")
	assertContains(t, w.Body.String(), `name="save_conflict_review"`)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Current authorized value")
	reviewed := regexp.MustCompile(`name="save_conflict_review" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(reviewed) != 2 {
		t.Fatal("missing explicit reviewed revision")
	}
	values.Set("save_action_id", newOperationKey())
	values.Set("save_conflict_review", reviewed[1])
	itemPost(t, router, cookie, path, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "My retained correction")
}

func TestGuardedConfigurationConflictRequiresExplicitNativeReviewHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	old := configurationRevision(t, requestBody(t, router, cookie, "GET", "/settings", 200))
	settingsPost(t, router, cookie, url.Values{"section": {"company"}, "revision": {old}, "business_name": {"New current company"}}, 303)
	values := url.Values{"section": {"company"}, "revision": {old}, "business_name": {"Retained company correction"}, "save_action_id": {newOperationKey()}}
	r := httptest.NewRequest("POST", "/settings", strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("conflict %d %s", w.Code, w.Body.String())
	}
	assertContains(t, w.Body.String(), "New current company")
	assertContains(t, w.Body.String(), "Retained company correction")
	reviewed := regexp.MustCompile(`name="save_native_revision_review" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(reviewed) != 2 {
		t.Fatal("missing native review submitter")
	}
	values.Set("save_action_id", newOperationKey())
	values.Set("save_native_revision_review", reviewed[1])
	settingsPost(t, router, cookie, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", "/settings", 200), "Retained company correction")
}

func TestGuardedConcurrentIdentityAndAuditRollbackHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	post := func(id, theme string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/preferences", strings.NewReader(url.Values{"save_action_id": {id}, "theme": {theme}, "font_size": {"large"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	id := newOperationKey()
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { results <- post(id, "dark") }()
	}
	a, b := <-results, <-results
	if !(a.Code == 303 && b.Code == 409 || a.Code == 409 && b.Code == 303) {
		t.Fatalf("concurrent admission %d %d", a.Code, b.Code)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+id, 200), `"committed_steps":1`)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_guarded_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='presentation.edit' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_guarded_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_guarded_audit()`); err != nil {
		t.Fatal(err)
	}
	failed := newOperationKey()
	if w := post(failed, "light"); w.Code != 409 {
		t.Fatalf("audit failure %d %s", w.Code, w.Body.String())
	}
	receipt := requestBody(t, router, cookie, "GET", "/save-actions/"+failed, 200)
	assertContains(t, receipt, `"disposition":"rejected"`)
	assertContains(t, receipt, `"committed_steps":0`)
	assertContains(t, requestBody(t, router, cookie, "GET", "/preferences", 200), `data-theme="dark"`)
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_guarded_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if w := post(failed, "light"); w.Code != 409 {
		t.Fatalf("terminal rejection reexecuted %d", w.Code)
	}
	if w := post(newOperationKey(), "light"); w.Code != 303 {
		t.Fatalf("explicit correction %d %s", w.Code, w.Body.String())
	}
}

func TestGuardedWaitingStaleSavePrefersCurrentPermissionDenialHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Waiting Item editor", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "item.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Waiting editor").SetEmail("waiting-save@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	item := client.Item.Create().SetCompanyID(company).SetName("Before").SaveX(ctx)
	path := fmt.Sprintf("/items/%d", item.ID)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	revision := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(form)
	if len(revision) != 2 {
		t.Fatal("missing revision")
	}
	currentName := "Current Item"
	if _, err := svc.SaveItem(ctx, authorization.Actor{UserID: admin}, item.ID, authorization.ItemInput{Name: &currentName}); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	var pid int
	if err := blocker.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	id := newOperationKey()
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", path, strings.NewReader(url.Values{"save_action_id": {id}, "save_revision": {revision[1]}, "name": {"MUST-NOT-RETAIN"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("save did not wait at current-authority fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := blocker.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='item.edit'`, role); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != 403 {
		t.Fatalf("stale denial %d %s", w.Code, w.Body.String())
	}
	assertNotContains(t, w.Body.String(), "MUST-NOT-RETAIN")
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Current Item")
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/save-actions/"+id, 200), "MUST-NOT-RETAIN")
}
