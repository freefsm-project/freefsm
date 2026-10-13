package handlers

import (
	"context"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent/project"
	"github.com/freefsm-project/freefsm/internal/ent/status"
)

func TestProjectMinimalCreationAndImmutableCustomerHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Project owner").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other owner").SaveX(ctx)
	masterPost(t, router, cookie, "/projects", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Minimal project"}, "completion_percentage": {"75"}}, 303)
	p := client.Project.Query().Where(project.NameEQ("Minimal project")).OnlyX(ctx)
	if p.CompletionPercentage != 0 {
		t.Fatalf("creation progress = %v, want 0", p.CompletionPercentage)
	}
	masterPost(t, router, cookie, fmt.Sprintf("/projects/%d", p.ID), url.Values{"customer_id": {fmt.Sprint(other.ID)}, "name": {"Moved"}, "completion_percentage": {"0"}}, 400)
	if got := client.Project.GetX(ctx, p.ID); got.CustomerID != c.ID || got.Name != "Minimal project" {
		t.Fatal("rejected ownership change modified project")
	}
}

func TestProjectContractPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser Project Customer").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/project-contract.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}

func TestProjectLocationRepairAndIndependentJobHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Project site").SaveX(ctx)
	jobLoc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Independent Job site").SaveX(ctx)
	masterPost(t, router, cookie, "/projects", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Location repair"}, "location_id": {fmt.Sprint(loc.ID)}}, 303)
	p := client.Project.Query().Where(project.NameEQ("Location repair")).OnlyX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetLocationID(jobLoc.ID).SetJobType("Independent location").SaveX(ctx)
	path := fmt.Sprintf("/projects/%d", p.ID)
	completed := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("project:completed")).FirstX(ctx)
	masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(completed.ID)}}, 303)
	masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/locations/%d/delete", c.ID, loc.ID), nil, 303)
	masterPost(t, router, cookie, path+"/delete", nil, 303)
	masterPost(t, router, cookie, path+"/restore", nil, 303)
	masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(*p.StatusID)}}, 409)
	masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "location_id": {"0"}}, 303)
	masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(*p.StatusID)}}, 303)
	if got := client.Job.GetX(ctx, j.ID); got.LocationID == nil || *got.LocationID != jobLoc.ID {
		t.Fatal("Project changed Job Location")
	}
}

func TestProjectWaitingEditRechecksCompletionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	masterPost(t, router, cookie, "/projects", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Race"}}, 303)
	p := client.Project.Query().Where(project.NameEQ("Race")).OnlyX(ctx)
	path := fmt.Sprintf("/projects/%d", p.ID)
	masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "completion_percentage": {"37"}}, 303)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var pid int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) locked`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "completion_percentage": {"100"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w.Code
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("edit did not reach fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`); err != nil {
		t.Fatal(err)
	}
	// Commit the competing semantic change while the editor waits on the fence.
	if _, err = tx.Exec(ctx, `UPDATE projects SET status_id=(SELECT id FROM statuses WHERE company_id=$2 AND category_key='project:completed' LIMIT 1) WHERE id=$1`, p.ID, company); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 409 {
		t.Fatalf("stale progress edit returned %d", code)
	}
	if got := client.Project.GetX(ctx, p.ID); got.CompletionPercentage != 37 {
		t.Fatal("derived 100 overwrote manual progress")
	}
}

func TestProjectAuditFailureAndUnauthorizedLinksRollbackHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(other.ID).SetTitle("Foreign Customer location").SaveX(ctx)
	masterPost(t, router, cookie, "/projects", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Atomic"}}, 303)
	p := client.Project.Query().Where(project.NameEQ("Atomic")).OnlyX(ctx)
	path := fmt.Sprintf("/projects/%d", p.ID)
	masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Rejected"}, "completion_percentage": {"71"}, "location_id": {fmt.Sprint(loc.ID)}}, 404)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_project_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='project' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_project_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_project_audit()`); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Rejected"}, "completion_percentage": {"71"}}, 500)
	if got := client.Project.GetX(ctx, p.ID); got.Name != "Atomic" || got.CompletionPercentage != 0 || got.LocationID != nil {
		t.Fatal("failed action partially changed Project")
	}
}

func TestProjectIndependentPermissionsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Hidden Customer").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Independent editor").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Hidden Location").SaveX(ctx)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Project editor only", []authorization.Grant{{Permission: "project.view", Scope: authorization.All}, {Permission: "project.edit", Scope: authorization.All}, {Permission: "project.create", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("projecteditor@test").SetName("Editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/projects/%d", p.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertNotContains(t, body, "Hidden Customer")
	masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Edited independently"}, "completion_percentage": {"100"}}, 303)
	masterPost(t, router, cookie, path+"/delete", nil, 403)
	masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {"1"}}, 403)
	masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Rejected"}, "location_id": {fmt.Sprint(loc.ID)}}, 404)
	masterPost(t, router, cookie, "/projects", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Unauthorized Customer"}}, 404)
}

func TestProjectCommitRechecksRacingInputsAndGrantHTTP(t *testing.T) {
	for _, kind := range []string{"customer", "location", "grant"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race owner").SaveX(ctx)
			loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Race site").SaveX(ctx)
			svc := authorization.New(pool)
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Project creator", []authorization.Grant{{Permission: "project.view", Scope: authorization.All}, {Permission: "project.create", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "location.view", Scope: authorization.All}})
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetEmail("racingproject@test").SetName("Creator").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var pid int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) locked`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				r := httptest.NewRequest("POST", "/projects", strings.NewReader(url.Values{"customer_id": {fmt.Sprint(c.ID)}, "location_id": {fmt.Sprint(loc.ID)}, "name": {"Must not commit"}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w.Code
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				var waiting bool
				if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("creation did not reach fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch kind {
			case "customer":
				_, err = tx.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, c.ID)
			case "location":
				_, err = tx.Exec(ctx, `UPDATE locations SET deleted_at=now() WHERE id=$1`, loc.ID)
			case "grant":
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='project.create'`, role)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if code := <-done; code < 400 {
				t.Fatalf("stale %s action accepted: %d", kind, code)
			}
			if client.Project.Query().Where(project.NameEQ("Must not commit")).ExistX(ctx) {
				t.Fatal("rejected action persisted")
			}
		})
	}
}

func TestProjectAssignedContextIsClosedHTTPAndAPI(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Allowed Project").SetDescription("Allowed description").SetServiceNotes("Allowed service instructions").SetNotes("SECRET_INTERNAL").SetCustomFields(`{"secret":"SECRET_FIELD"}`).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetJobType("Assigned").SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetJobType("SECRET_SIBLING").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetEmail("projectcontext@test").SetName("Field").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetUserID(u.ID).SetJobID(j.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	token, err := sessions.CreateMobile(ctx, u.ID, "Project context device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	get := func(want int) string {
		r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("context %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	body := get(200)
	assertContains(t, body, "Allowed Project")
	assertContains(t, body, "Allowed service instructions")
	assertNotContains(t, body, "SECRET_")
	web := requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d/customer-context", j.ID), 200)
	assertContains(t, web, "Allowed Project")
	assertNotContains(t, web, "SECRET_")
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/projects/%d", p.ID), 403)
	if _, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	get(404)
}

func TestProjectCompletionRetainsManualProgressAndLifecycleHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	masterPost(t, router, cookie, "/projects", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Completion"}}, 303)
	p := client.Project.Query().Where(project.NameEQ("Completion")).OnlyX(ctx)
	path := fmt.Sprintf("/projects/%d", p.ID)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "completion_percentage": {"37"}}
	values.Set("completion_percentage", "100")
	masterPost(t, router, cookie, path, values, 303)
	masterPost(t, router, cookie, path+"/delete", nil, 409)
	values.Set("completion_percentage", "37")
	masterPost(t, router, cookie, path, values, 303)
	masterPost(t, router, cookie, path+"/delete", nil, 409)
	completed := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("project:completed")).FirstX(ctx)
	for range 2 {
		masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(completed.ID)}}, 303)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "100%")
	values.Set("completion_percentage", "100")
	masterPost(t, router, cookie, path, values, 409)
	// An ongoing direct Job is independent of Project archive.
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetJobType("Still working").SaveX(ctx)
	masterPost(t, router, cookie, path+"/delete", nil, 303)
	masterPost(t, router, cookie, path+"/restore", nil, 303)
	masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(*p.StatusID)}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "37%")
}

func TestProjectProgressAndDatesRejectInvalidHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	masterPost(t, router, cookie, "/projects", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Validation"}}, 303)
	p := client.Project.Query().Where(project.NameEQ("Validation")).OnlyX(ctx)
	path := fmt.Sprintf("/projects/%d", p.ID)
	for _, progress := range []string{"-1", "101", "2.5", "NaN", "bad"} {
		masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "completion_percentage": {progress}}, 400)
	}
	masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "location_id": {"invalid"}}, 400)
	for _, dates := range [][2]string{{"2026-02-30", ""}, {"2026-05-02", "2026-05-01"}} {
		masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "start_time": {dates[0]}, "end_time": {dates[1]}}, 400)
	}
	masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "completion_percentage": {"37"}, "start_time": {"2026-05-01"}, "end_time": {"2026-05-02"}}, 303)
	body := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	assertContains(t, body, `value="37"`)
	assertContains(t, body, `value="2026-05-02"`)
	for _, zone := range []string{"Pacific/Auckland", "America/Los_Angeles"} {
		if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone=$2 WHERE company_id=$1`, company, zone); err != nil {
			t.Fatal(err)
		}
		body = requestBody(t, router, cookie, "GET", path, 200)
		assertContains(t, body, "2026-05-01")
		assertContains(t, body, "2026-05-02")
	}
	masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "start_time": {""}, "end_time": {""}}, 303)
	if got := client.Project.GetX(ctx, p.ID); got.PlannedStartDate != nil || got.PlannedEndDate != nil || got.CompletionPercentage != 37 {
		t.Fatal("clearing dates must retain omitted progress")
	}
}

func TestProjectDirectJobsPaginationHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Direct jobs").SaveX(ctx)
	for i := range 26 {
		client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetJobType(fmt.Sprintf("Direct job %02d", i)).SaveX(ctx)
	}
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unrelated job").SaveX(ctx)
	path := fmt.Sprintf("/projects/%d", p.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Direct job 00")
	assertNotContains(t, body, "Direct job 25")
	assertNotContains(t, body, "Unrelated job")
	body = requestBody(t, router, cookie, "GET", path+"?jobs_page=2", 200)
	assertContains(t, body, "Direct job 25")
	assertNotContains(t, body, "Direct job 00")
}
