package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
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
