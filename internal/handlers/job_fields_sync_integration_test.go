package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestJobFieldsSyncDefinitionsAndRevisionsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Field sync customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Field sync job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Reading"}, "object_type": {"job"}, "field_type": {"number"}, "precision": {"2"}}, 303)
	token, err := sessions.CreateMobile(ctx, admin, "Field sync")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path, body string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var result struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data
	}
	page := call("GET", "/sync/jobs", "")
	record := page["records"].([]any)[0].(map[string]any)
	fields, ok := record["custom_fields"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot lacks field definitions/values/revisions: %v", record)
	}
	defs := fields["definitions"].([]any)
	if len(defs) != 1 || defs[0].(map[string]any)["precision"] != float64(2) || fields["can_edit"] != true {
		t.Fatalf("field metadata: %v", fields)
	}
	initial := fields["revisions"].(map[string]any)["1"].(string)
	call("PATCH", fmt.Sprintf("/jobs/%v/custom-fields", record["id"]), fmt.Sprintf(`{"operation_id":"82000000-0000-4000-8000-000000000001","revisions":{"1":%q},"values":[{"definition_id":1,"value":"12.50"}]}`, initial))
	delta := call("GET", "/sync/jobs?cursor="+url.QueryEscape(page["completion_cursor"].(string)), "")
	changed := delta["records"].([]any)[0].(map[string]any)["custom_fields"].(map[string]any)
	if changed["revisions"].(map[string]any)["1"] == initial {
		t.Fatal("changed value did not advance revision")
	}
	if len(changed["history"].([]any)) != 1 {
		t.Fatalf("synchronized field history: %v", changed)
	}
	unchanged := call("GET", "/sync/jobs?cursor="+url.QueryEscape(delta["completion_cursor"].(string)), "")
	if len(unchanged["records"].([]any)) != 0 {
		t.Fatalf("unchanged field state must not manufacture a delta: %v", unchanged)
	}
}

func TestJobFieldsBatchAtomicReplayAndDependenciesHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Batch fields").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Batch fields").SaveX(ctx)
	for _, name := range []string{"First", "Second"} {
		fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {name}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Field batch")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	var initial struct {
		Data authorization.FieldState `json:"data"`
	}
	json.Unmarshal([]byte(call("GET", fmt.Sprintf("/jobs/%d/custom-fields", j.ID), "", 200)), &initial)
	first := fmt.Sprintf(`{"operation_id":"82000000-0000-4000-8000-000000000010","type":"job.fields","target":%q,"depends_on":[],"revisions":{"1":%q},"values":[{"definition_id":1,"value":"A"}]}`, fmt.Sprint(j.ID), initial.Data.Revisions["1"])
	stale := fmt.Sprintf(`{"operation_id":"82000000-0000-4000-8000-000000000011","type":"job.fields","target":%q,"depends_on":["82000000-0000-4000-8000-000000000010"],"revisions":{"1":%q,"2":%q},"values":[{"definition_id":1,"value":"stale"},{"definition_id":2,"value":"must roll back"}]}`, fmt.Sprint(j.ID), initial.Data.Revisions["1"], initial.Data.Revisions["2"])
	body := `{"operations":[` + first + `,` + stale + `]}`
	foreignPayload := strings.Replace(stale, `"revisions":`, `"notes_revision":"","revisions":`, 1)
	call("POST", "/sync/operations", `{"operations":[`+first+`,`+foreignPayload+`]}`, 400)
	result := call("POST", "/sync/operations", body, 200)
	assertContains(t, result, `"code":"accepted"`)
	assertContains(t, result, "stale_field_revision")
	replay := call("POST", "/sync/operations", body, 200)
	assertContains(t, replay, `"code":"accepted"`)
	assertContains(t, replay, "stale_field_revision")
	assertNotContains(t, call("GET", fmt.Sprintf("/jobs/%d/custom-fields", j.ID), "", 200), "must roll back")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1 AND action='job.fields.edit'`, j.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count %d: %v", count, err)
	}
}

func TestJobFieldsTypedValidationReasonHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Typed reasons").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Typed reasons").SaveX(ctx)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Number"}, "object_type": {"job"}, "field_type": {"number"}, "precision": {"2"}}, 303)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Choice"}, "object_type": {"job"}, "field_type": {"select"}, "options": {`["A","B"]`}}, 303)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Checkbox"}, "object_type": {"job"}, "field_type": {"checkbox"}, "required": {"true"}}, 303)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Date"}, "object_type": {"job"}, "field_type": {"date"}}, 303)
	token, err := sessions.CreateMobile(ctx, admin, "Field reason")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for _, tc := range []struct {
		id            int
		value, reason string
	}{
		{1, `"12.345"`, "invalid_number"}, {1, `"not-number"`, "invalid_number"}, {1, `[]`, "invalid_field_type"},
		{2, `"missing"`, "invalid_option"}, {3, `""`, "required_field"}, {3, `"maybe"`, "invalid_checkbox"}, {4, `"2026-02-30"`, "invalid_date"},
	} {
		operation := newOperationKey()
		body := fmt.Sprintf(`{"operation_id":%q,"revisions":{"%d":"00000000-0000-0000-0000-000000000000"},"values":[{"definition_id":%d,"value":%s}]}`, operation, tc.id, tc.id, tc.value)
		r := httptest.NewRequest("PATCH", fmt.Sprintf("/jobs/%d/custom-fields", j.ID), strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 409 {
			t.Fatalf("invalid number: %d %s", w.Code, w.Body.String())
		}
		assertContains(t, w.Body.String(), `"reason":"`+tc.reason+`"`)
		lookup := httptest.NewRequest("GET", "/job-field-operations/"+operation, nil)
		lookup.Header.Set("Authorization", "Bearer "+token)
		receipt := httptest.NewRecorder()
		api.ServeHTTP(receipt, lookup)
		if receipt.Code != 200 {
			t.Fatalf("receipt: %d %s", receipt.Code, receipt.Body.String())
		}
		assertContains(t, receipt.Body.String(), `"reason":"`+tc.reason+`"`)
	}
}
