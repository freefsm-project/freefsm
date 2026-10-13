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
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestOperationalActivityFieldAPIHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API activity customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("API activity job").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action,metadata) VALUES($1,$2,'job',$3,'field_notes_updated','{"before":"API original observation","after":"API revised observation","token":"HIDDEN_TOKEN"}')`, company, admin, j.ID); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Activity API")
	if err != nil {
		t.Fatal(err)
	}
	router := apiv1.NewRouter(pool, client, sessions)
	get := func(path string, want int) string {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	body := get(fmt.Sprintf("/jobs/%d/activity", j.ID), 200)
	assertContains(t, body, "API revised observation")
	assertNotContains(t, body, "HIDDEN_TOKEN")
	var spec map[string]any
	if err := json.Unmarshal([]byte(get("/openapi.json", 200)), &spec); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://activity.test/openapi.json", spec); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://activity.test/openapi.json#/components/schemas/ActivityEnvelope")
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("activity response contract: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action) SELECT $1,$2,'job',$3,'updated' FROM generate_series(1,55)`, company, admin, j.ID); err != nil {
		t.Fatal(err)
	}
	var first, second struct {
		Data struct {
			Records []struct {
				ID int64 `json:"id"`
			} `json:"records"`
			Next string `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(get(fmt.Sprintf("/jobs/%d/activity?action=updated&limit=1", j.ID), 200)), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Data.Records) != 1 || first.Data.Next == "" {
		t.Fatal("missing bounded page/cursor")
	}
	if err := json.Unmarshal([]byte(get(fmt.Sprintf("/jobs/%d/activity?action=updated&limit=1&cursor=%s", j.ID, url.QueryEscape(first.Data.Next)), 200)), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Data.Records) != 1 || second.Data.Records[0].ID >= first.Data.Records[0].ID {
		t.Fatal("cursor repeated or reordered activity")
	}
	get(fmt.Sprintf("/jobs/%d/activity?action=created&cursor=%s", j.ID, url.QueryEscape(first.Data.Next)), 400)
	get(fmt.Sprintf("/jobs/%d/activity?unknown=1", j.ID), 400)
	get(fmt.Sprintf("/jobs/%d/activity?limit=201", j.ID), 400)
	get("/activity/export", 404)
	get("/exports", 404)
	get(fmt.Sprintf("/customers/%d/activity", c.ID), 404)
	if _, err := pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	get(fmt.Sprintf("/jobs/%d/activity", j.ID), 404)
}
