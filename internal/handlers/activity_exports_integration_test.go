package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestActivityExportDurableSnapshotHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Activity CSV subject").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action,created_at) SELECT $1,$2,'customer',$3,'updated','2026-02-01T12:00:00Z'::timestamptz+n*interval '1 second' FROM generate_series(1,105) n`, company, admin, customer.ID); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", "/activity/export?from=2026-02-01", 200), "All matching activity")
	values := url.Values{"operation_id": {newOperationKey()}, "from": {"2026-02-01"}}
	path := masterPost(t, router, cookie, "/activity/export", values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Pending")
	if replay := masterPost(t, router, cookie, "/activity/export", values, 303); replay != path {
		t.Fatalf("duplicate export %s != %s", replay, path)
	}
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Ready")
	rows, err := csv.NewReader(strings.NewReader(requestBody(t, router, cookie, "GET", path+"/download", 200))).ReadAll()
	if err != nil || len(rows) != 106 {
		t.Fatalf("all matching snapshot rows: %d %v", len(rows), err)
	}
	if rows[0][0] != "activity_id" {
		t.Fatalf("fixed columns: %v", rows[0])
	}
	assertContains(t, strings.Join(rows[1], "|"), "Activity CSV subject")
}

func TestActivityCurrentFacetsFilterBeforePaginationHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Activity reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "activity.export", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Original field author").SetEmail("activity-reader@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Activity context").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Visible field job").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2);`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action,metadata,created_at) VALUES($1,$2,'job',$3,'field_notes_updated','{"before":"Original field observation","after":"Revised field observation","token":"NEVER_A_TOKEN","comment_preview":"REMOVED_TEXT"}','2026-01-01')`, company, u.ID, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action,created_at) SELECT $1,$2,'job',$3,'clocked_in','2026-02-01'::timestamptz+n*interval '1 second' FROM generate_series(1,30)n`, company, u.ID, j.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", "/activity", 200)
	assertContains(t, body, "Original field observation")
	assertContains(t, body, "Revised field observation")
	assertNotContains(t, body, "clocked in")
	assertNotContains(t, body, "Older</a>")
	assertNotContains(t, body, "NEVER_A_TOKEN")
	assertNotContains(t, body, "REMOVED_TEXT")
	path := masterPost(t, router, cookie, "/activity/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path+"/download", 200), "Revised field observation")
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	requestBody(t, router, cookie, "GET", path+"/download", 410)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/activity", 200), "Revised field observation")
	requestBody(t, router, cookie, "GET", fmt.Sprintf("/activity?object_type=job&object_id=%d", j.ID), 403)
}

func TestActivityJobFieldHistoryAtomicHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Field history customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Field history job").SaveX(ctx)
	fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Observation"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	path := fmt.Sprintf("/jobs/%d/custom-fields", j.ID)
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"Original persisted observation"}}, 303)
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"Revised persisted observation"}}, 303)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d/activity", j.ID), 200)
	assertContains(t, body, "Original persisted observation")
	assertContains(t, body, "Revised persisted observation")
	if _, err := pool.Exec(ctx, `UPDATE activity_logs SET metadata='{}' WHERE object_type='job' AND object_id=$1`, j.ID); err == nil {
		t.Fatal("operational history was mutable")
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_field_activity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='job_fields_changed' THEN RAISE EXCEPTION 'history storage unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_field_activity BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION reject_field_activity()`); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, path, url.Values{"custom_field_1": {"Rejected field value"}}, 409)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Revised persisted observation")
	assertNotContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d/activity", j.ID), 200), "Rejected field value")
}
