package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/status"
)

func reviewedJobFieldsForm(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values) url.Values {
	t.Helper()
	form := requestBody(t, router, cookie, "GET", path, 200)
	result := url.Values{}
	for key, value := range values {
		result[key] = append([]string(nil), value...)
	}
	for _, key := range []string{"field_operation_id", "field_baseline"} {
		match := regexp.MustCompile(`name="` + key + `" value="([^"]*)"`).FindStringSubmatch(form)
		if len(match) != 2 {
			t.Fatalf("missing %s", key)
		}
		result.Set(key, html.UnescapeString(match[1]))
	}
	return result
}

func TestJobFieldsDeniedOperationStaysTerminalAfterAssignmentRepair(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Terminal customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Terminal job").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetEmail("terminal-field@test").SetName("Field worker").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Reading"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	token, err := sessions.CreateMobile(ctx, u.ID, "Terminal client")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d/custom-fields", j.ID)
	call := func(method, target, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d want %d: %s", method, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	var state struct {
		Data authorization.FieldState `json:"data"`
	}
	if err := json.Unmarshal([]byte(call("GET", path, "", 200)), &state); err != nil {
		t.Fatal(err)
	}
	id := "41000000-0000-4000-8000-000000000001"
	input := fmt.Sprintf(`{"operation_id":%q,"revisions":{"1":%q},"values":[{"definition_id":1,"value":"must remain pending"}]}`, id, state.Data.Revisions["1"])
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	assertNotContains(t, call("PATCH", path, input, 404), "comparison")
	assertContains(t, call("GET", path+"?operation_id="+id, "", 200), "not_found")
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	call("PATCH", path, input, 404)
	assertNotContains(t, call("GET", path, "", 200), "must remain pending")
}

func TestJobFieldsDisjointAndABAWeb(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Disjoint customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Independent values").SaveX(ctx)
	for _, name := range []string{"First", "Second"} {
		fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {name}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	}
	path := fmt.Sprintf("/jobs/%d/custom-fields", job.ID)
	a := reviewedJobFieldsForm(t, router, cookie, path, url.Values{"custom_field_1": {"A"}, "custom_field_2": {""}})
	b := reviewedJobFieldsForm(t, router, cookie, path, url.Values{"custom_field_1": {""}, "custom_field_2": {"B"}})
	fieldPost(t, router, cookie, path, a, 303)
	fieldPost(t, router, cookie, path, b, 303)
	stale := reviewedJobFieldsForm(t, router, cookie, path, url.Values{"custom_field_1": {"stale"}})
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"changed"}}, 303)
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"A"}}, 303)
	conflict := fieldPost(t, router, cookie, path, stale, 409)
	assertContains(t, conflict.Body.String(), "stale_field_revision")
	assertContains(t, conflict.Body.String(), "Review and save as new operation")
	page := requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d", job.ID), 200)
	assertNotContains(t, page, "stale")
	assertContains(t, page, ">A</p>")
	assertContains(t, page, ">B</p>")
}

func TestJobFieldsRequireReviewedOperationHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reviewed fields").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Reviewed job").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Reviewed field client")
	if err != nil {
		t.Fatal(err)
	}
	router := apiv1.NewRouter(pool, client, sessions)
	r := httptest.NewRequest("PATCH", fmt.Sprintf("/jobs/%d/custom-fields", job.ID), strings.NewReader(`{"values":[]}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "invalid_operation_id") {
		t.Fatalf("unguarded patch must reject: %d %s", w.Code, w.Body.String())
	}
}

func TestJobFieldsWebRejectsUnguardedSave(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Web fields").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Guarded web job").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Reading"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	r := httptest.NewRequest("POST", fmt.Sprintf("/jobs/%d/custom-fields", job.ID), strings.NewReader("custom_field_1=unguarded"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("unguarded web save must reject: %d %s", w.Code, w.Body.String())
	}
	assertNotContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d", job.ID), 200), "unguarded")
}

func TestJobFieldsPlanningRequiresFieldRevisionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Planning fields").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Original title").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Reading"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	path := fmt.Sprintf("/jobs/%d", job.ID)
	values := currentJobFormValues(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(job.ID)}, "customer_id": {fmt.Sprint(customer.ID)}, "job_type": {"Must roll back"}, "custom_field_1": {"unguarded"}})
	values.Del("field_baseline")
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("missing field revision: %d %s", w.Code, w.Body.String())
	}
	page := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, page, "Original title")
	assertNotContains(t, page, "unguarded")
}

func TestJobFieldsMissingBaselineCannotAcknowledgeAnUnappliedClear(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Clear customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Clear job").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Reading"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	path := fmt.Sprintf("/jobs/%d/custom-fields", j.ID)
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"Recorded value"}}, 303)
	// A missing original cannot mean that an explicit clear was unchanged.
	fieldPost(t, router, cookie, path, url.Values{"field_operation_id": {newOperationKey()}, "custom_field_1": {""}}, 409)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Recorded value")
}

func TestJobFieldsRequirednessDoesNotBlockMinimalCreationOrLifecycleHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Lifecycle customer").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"New required reading"}, "object_type": {"job"}, "field_type": {"text"}, "required": {"true"}}, 303)
	fieldPost(t, router, cookie, "/jobs", url.Values{"form_mode": {"create"}, "job_id": {"0"}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Minimal required Job"}}, 303)
	j := client.Job.Query().Where(job.JobTypeEQ("Minimal required Job")).OnlyX(ctx)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	fieldPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Unrelated details must validate"}}, 422)
	completed := client.Status.Query().Where(status.CategoryKeyEQ("job:completed")).OnlyX(ctx)
	fieldPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(completed.ID)}}, 303)
	fieldPost(t, router, cookie, path+"/delete", url.Values{}, 303)
	fieldPost(t, router, cookie, path+"/restore", url.Values{}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Minimal required Job")
	if got := client.Job.GetX(ctx, j.ID); got.CustomFields != "[]" || got.DeletedAt != nil {
		t.Fatalf("lifecycle backfilled or failed restore: %+v", got)
	}
}
