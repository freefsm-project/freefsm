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
)

func TestFieldRevisionsExposeOnlyOwnedAndRetainedGlobalDefinitionsAPI(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"object_type": {"job"}, "name": {"Owned reading"}, "field_type": {"text"}}, 303)
	var other int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other company','foreign-fields') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO custom_field_definitions(id,company_id,object_type,name,field_type) VALUES(8000,NULL,'job','Retained global reading','text'),(8001,$1,'job','FOREIGN DEFINITION','text')`, other); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Scoped fields").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Scoped revisions").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Scoped fields")
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
			t.Fatalf("%s got %d want %d: %s", method, w.Code, want, w.Body.String())
		}
		var response struct {
			Data struct {
				Revisions map[string]string `json:"revisions"`
			} `json:"data"`
			Comparison struct {
				Revisions map[string]string `json:"revisions"`
			} `json:"comparison"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		revisions := response.Data.Revisions
		if want == 409 {
			revisions = response.Comparison.Revisions
		}
		if len(revisions) != 2 || revisions["1"] == "" || revisions["8000"] == "" || revisions["8001"] != "" {
			t.Fatalf("revision disclosure: %+v", revisions)
		}
		assertNotContains(t, w.Body.String(), "FOREIGN DEFINITION")
		return w.Body.String()
	}
	call("GET", "", 200)
	input := `{"operation_id":"30000000-0000-4000-8000-000000000301","revisions":{"1":"00000000-0000-0000-0000-000000000000","8000":"00000000-0000-0000-0000-000000000000"},"values":[{"definition_id":1,"value":"Owned"},{"definition_id":8000,"value":"Global"}]}`
	call("PATCH", input, 200)
	call("PATCH", input, 200)
	call("GET", "", 200)
	call("PATCH", strings.Replace(input, "000000000301", "000000000302", 1), 409)
	foreign := `{"operation_id":"30000000-0000-4000-8000-000000000303","revisions":{"8001":"00000000-0000-0000-0000-000000000000"},"values":[{"definition_id":8001,"value":"Not authorized"}]}`
	denied := call("PATCH", foreign, 409)
	unknown := strings.ReplaceAll(strings.Replace(foreign, "000000000303", "000000000304", 1), "8001", "8999")
	// Request/operation correlation IDs are intentionally different. Compare all
	// remaining authorized representation and error fields for nondisclosure.
	stable := func(raw string) string {
		var value map[string]any
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			t.Fatal(err)
		}
		delete(value["error"].(map[string]any), "request_id")
		if receipt, ok := value["comparison"].(map[string]any)["receipt"].(map[string]any); ok {
			delete(receipt, "operation_id")
		}
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}
	if missing := call("PATCH", unknown, 409); stable(missing) != stable(denied) {
		t.Fatalf("foreign field existence leaked: %s != %s", denied, missing)
	}
}
