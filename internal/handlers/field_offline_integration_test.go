package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFieldOfflineTerminalRetirementAndStaleRevisionAPI(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Offline customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Offline fields").SaveX(ctx)
	definition := url.Values{"name": {"Reading"}, "object_type": {"job"}, "field_type": {"text"}}
	fieldPost(t, router, cookie, "/settings/custom-fields", definition, 303)
	token, err := sessions.CreateMobile(ctx, admin, "Field review")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d/custom-fields", job.ID)
	call := func(method, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d: %s", method, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	var state struct {
		Data struct {
			Revisions map[string]string `json:"revisions"`
		} `json:"data"`
	}
	if err = json.Unmarshal([]byte(call("GET", "", 200)), &state); err != nil {
		t.Fatal(err)
	}
	revision := state.Data.Revisions["1"]
	if revision == "" {
		t.Fatal("missing per-field revision")
	}
	input := fmt.Sprintf(`{"operation_id":"30666666-6666-4666-8666-666666666666","revisions":{"1":%q},"values":[{"definition_id":1,"value":"Pending"}]}`, revision)
	definition.Set("retired", "true")
	fieldPost(t, router, cookie, "/settings/custom-fields/1", definition, 303)
	assertContains(t, call("PATCH", input, 409), "new_reviewed_operation")
	definition.Set("retired", "false")
	fieldPost(t, router, cookie, "/settings/custom-fields/1", definition, 303)
	assertContains(t, call("PATCH", input, 409), "new_reviewed_operation")
	assertNotContains(t, call("GET", "", 200), "Pending")
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"Online change"}}, 303)
	stale := strings.Replace(input, "30666666-6666-4666-8666-666666666666", "30777777-7777-4777-8777-777777777777", 1)
	assertContains(t, call("PATCH", stale, 409), "stale_field_revision")
	assertContains(t, call("GET", "", 200), "Online change")
	if err = json.Unmarshal([]byte(call("GET", "", 200)), &state); err != nil {
		t.Fatal(err)
	}
	definition.Set("name", "Cosmetic reading label")
	definition.Set("required", "true")
	fieldPost(t, router, cookie, "/settings/custom-fields/1", definition, 303)
	valid := fmt.Sprintf(`{"operation_id":"30999999-9999-4999-8999-999999999999","revisions":{"1":%q},"values":[{"definition_id":1,"value":"Reviewed value"}]}`, state.Data.Revisions["1"])
	assertContains(t, call("PATCH", valid, 200), "Reviewed value")
	assertContains(t, call("PATCH", valid, 200), "Reviewed value")
	path += "?operation_id=30666666-6666-4666-8666-666666666666"
	assertContains(t, call("GET", "", 200), "invalid_custom_field")
	assertNotContains(t, call("GET", "", 200), "Pending")
}

func TestFieldReceiptSurvivesScopeLossWithoutValuesAPI(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Receipt customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Receipt Job").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetEmail("field-receipt@test").SetName("Field reader").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Private field"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	token, err := sessions.CreateMobile(ctx, u.ID, "Receipt client")
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
			t.Fatalf("got %d want %d: %s", w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	input := `{"operation_id":"30000000-0000-4000-8000-000000000001","revisions":{"1":"00000000-0000-0000-0000-000000000000"},"values":[{"definition_id":1,"value":"Restricted result"}]}`
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_reviewed_field_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='job.fields.edit' THEN RAISE EXCEPTION 'audit storage unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_reviewed_field_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_reviewed_field_audit()`); err != nil {
		t.Fatal(err)
	}
	call("PATCH", path, input, 500)
	call("GET", path+"?operation_id=30000000-0000-4000-8000-000000000001", "", 404)
	assertNotContains(t, call("GET", path, "", 200), "Restricted result")
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_reviewed_field_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	call("PATCH", path, input, 200)
	if _, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	call("GET", path, "", 404)
	receipt := call("GET", path+"?operation_id=30000000-0000-4000-8000-000000000001", "", 200)
	assertContains(t, receipt, "accepted")
	assertNotContains(t, receipt, "Restricted result")
}
