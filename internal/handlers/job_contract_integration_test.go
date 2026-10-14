package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/job"
	"github.com/freefsm-project/freefsm/internal/ent/status"
)

func TestJobMinimalContractHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Job owner").SaveX(ctx)
	input := url.Values{"form_mode": {"create"}, "job_id": {"0"}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"   "}}
	masterPost(t, router, cookie, "/jobs", input, 400)
	input.Set("job_type", "Minimal work")
	masterPost(t, router, cookie, "/jobs", input, 303)
	j := client.Job.Query().Where(job.JobTypeEQ("Minimal work")).OnlyX(ctx)
	if j.StartTime != nil || j.ProjectID != nil || j.LocationID != nil || j.CustomerContactID != nil || j.AssetID != nil {
		t.Fatal("minimal creation silently planned or linked work")
	}
	if j.StatusID == nil || client.Status.GetX(ctx, *j.StatusID).CategoryKey != "job:new" {
		t.Fatal("minimal Job did not enter New")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d", j.ID), 200), "Minimal work")
}

func TestJobIndependentLinksAndCustomerReplacementHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original owner").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Replacement owner").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Site").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Work").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Planning only", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.edit", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "location.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Planner").SetEmail("planner33@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	input := url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Work"}, "location_id": {fmt.Sprint(l.ID)}}
	path := fmt.Sprintf("/jobs/%d", j.ID)
	masterPost(t, router, sessionCookie(t, ctx, sessions, u.ID), path, input, 403)
	cookie := sessionCookie(t, ctx, sessions, admin)
	masterPost(t, router, cookie, path, input, 303)
	first := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("First retained Contact").SaveX(ctx)
	second := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Second retained Contact").SaveX(ctx)
	masterPost(t, router, cookie, path+"/contacts", url.Values{"contact_ids": {fmt.Sprint(first.ID), fmt.Sprint(second.ID)}}, 303)
	input.Set("customer_id", fmt.Sprint(other.ID))
	input.Set("replace_customer", "true")
	// Clearing incompatible links is an inseparable effect of planning's
	// Customer replacement, not discretionary Link/Unlink authority (AUTH-02).
	masterPost(t, router, sessionCookie(t, ctx, sessions, u.ID), path, input, 303)
	got := client.Job.GetX(ctx, j.ID)
	if got.CustomerID != other.ID || got.LocationID != nil {
		t.Fatal("Customer replacement did not clear operational links atomically")
	}
	var audited bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE target_type='job' AND target_id=$1 AND actor_id=$2 AND action='job.edit' AND scope='A' AND detail->'before'->'contact_ids'=$3::jsonb AND detail->'after'->'contact_ids'='[]'::jsonb) AND NOT EXISTS(SELECT 1 FROM job_contacts WHERE job_id=$1)`, j.ID, u.ID, fmt.Sprintf("[%d,%d]", first.ID, second.ID)).Scan(&audited); err != nil || !audited {
		t.Fatalf("replacement history or clearing missing: %v", err)
	}
	linked := client.Estimate.Create().SetCompanyID(company).SetCustomerID(other.ID).SetJobID(j.ID).SetTitle("Dependency").SaveX(ctx)
	input.Set("customer_id", fmt.Sprint(c.ID))
	masterPost(t, router, cookie, path, input, 303)
	if client.Job.GetX(ctx, j.ID).CustomerID != c.ID || *client.Estimate.GetX(ctx, linked.ID).CustomerID != c.ID {
		t.Fatal("linked Estimate was not reassigned atomically with the Job")
	}
}

func TestJobMultipleExplicitContactsHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	a := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("First explicit").SaveX(ctx)
	b := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Second explicit").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Contacts").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/contacts", j.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "First explicit")
	masterPost(t, router, cookie, path, url.Values{"contact_ids": {fmt.Sprint(a.ID), fmt.Sprint(b.ID)}}, 303)
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, `value="`+fmt.Sprint(a.ID)+`" checked`)
	assertContains(t, body, `value="`+fmt.Sprint(b.ID)+`" checked`)
}

func TestJobPlanningWaiterRechecksAuthorityAndDependenciesHTTP(t *testing.T) {
	for _, race := range []string{"dependency", "archive", "session", "link_archive", "grant"} {
		t.Run(race, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original").SaveX(ctx)
			n := client.Customer.Create().SetCompanyID(company).SetDisplayName("Replacement").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained title").SaveX(ctx)
			l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Racing site").SaveX(ctx)
			var role int64
			if race == "grant" {
				auth := authorization.New(pool)
				var err error
				role, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Revocable link editor", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.relations.edit", Scope: authorization.All}, {Permission: "location.view", Scope: authorization.All}})
				if err != nil {
					t.Fatal(err)
				}
				u := client.User.Create().SetCompanyID(company).SetEmail("revocable33@test").SetName("Link editor").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
				if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
					t.Fatal(err)
				}
				cookie = sessionCookie(t, ctx, sessions, u.ID)
			}
			input := url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "job_type": {"Must roll back"}, "customer_id": {fmt.Sprint(n.ID)}, "replace_customer": {"true"}}
			path := fmt.Sprintf("/jobs/%d", j.ID)
			if race == "grant" || race == "link_archive" {
				path += "/links"
				input = url.Values{"project_id": {"0"}, "asset_id": {"0"}, "location_id": {fmt.Sprint(l.ID)}}
			}
			input = currentJobFormValues(t, router, cookie, path, input)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var pid int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) fence`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				r := httptest.NewRequest("POST", path, strings.NewReader(input.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w.Code
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				var waiting bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("request did not reach committing fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch race {
			case "dependency":
				_, err = tx.Exec(ctx, `INSERT INTO job_customer_blockers(job_id,service_entry_id) VALUES($1,33001)`, j.ID)
			case "archive":
				_, err = tx.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, n.ID)
			case "session":
				_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, admin)
			case "link_archive":
				_, err = tx.Exec(ctx, `UPDATE locations SET deleted_at=clock_timestamp() WHERE id=$1`, l.ID)
			case "grant":
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.relations.edit'`, role)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			code := <-done
			if race == "session" || race == "grant" {
				if code != 403 {
					t.Fatalf("revoked session=%d", code)
				}
			} else if code != 409 {
				t.Fatalf("%s=%d", race, code)
			}
			got := client.Job.GetX(ctx, j.ID)
			if got.CustomerID != c.ID || got.JobType != "Retained title" || got.LocationID != nil {
				t.Fatal("waiting rejected mutation partially committed")
			}
		})
	}
}

func TestJobRelationshipAuditFailureRollsBackHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Site").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Audit guarded").SaveX(ctx)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_job_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='job' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_job_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_job_audit()`); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/links", j.ID), url.Values{"project_id": {"0"}, "asset_id": {"0"}, "location_id": {fmt.Sprint(l.ID)}}, 500)
	if client.Job.GetX(ctx, j.ID).LocationID != nil {
		t.Fatal("audit failure published link")
	}
}

func TestJobPlanningPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser Job Customer").SaveX(ctx)
	a := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Browser Contact One").SaveX(ctx)
	b := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Browser Contact Two").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/job-planning.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID), fmt.Sprint(a.ID), fmt.Sprint(b.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}

func TestJobPlanningRelationshipsAreIndependentAndHistoricalHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Relationship curator", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.relations.edit", Scope: authorization.All}, {Permission: "project.view", Scope: authorization.All}, {Permission: "location.view", Scope: authorization.All}, {Permission: "contact.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("curator33@test").SetName("Curator").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Job site").SaveX(ctx)
	pl := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Project site").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(pl.ID).SetName("Historical Project").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Finished but actionable").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	masterPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "job_type": {"Forbidden planning"}}, 403)
	completed := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("job:completed")).FirstX(ctx)
	newStatus := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("job:new")).FirstX(ctx)
	masterPost(t, router, adminCookie, path+"/status", url.Values{"status_id": {fmt.Sprint(completed.ID)}}, 303)
	links := url.Values{"project_id": {fmt.Sprint(p.ID)}, "location_id": {fmt.Sprint(l.ID)}, "asset_id": {"0"}}
	masterPost(t, router, cookie, path+"/links", links, 303)
	got := client.Job.GetX(ctx, j.ID)
	if *got.LocationID != l.ID || *got.StatusID != completed.ID {
		t.Fatal("link action copied Project Location or changed Status")
	}
	if _, err := pool.Exec(ctx, `UPDATE projects SET deleted_at=clock_timestamp() WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, path+"/links", links, 303)
	masterPost(t, router, adminCookie, path+"/status", url.Values{"status_id": {fmt.Sprint(newStatus.ID)}}, 303)
	links.Set("project_id", "0")
	masterPost(t, router, cookie, path+"/links", links, 303)
	links.Set("project_id", fmt.Sprint(p.ID))
	masterPost(t, router, cookie, path+"/links", links, 409)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other Customer").SaveX(ctx)
	foreign := client.Project.Create().SetCompanyID(company).SetCustomerID(other.ID).SetName("SECRET_OTHER_PROJECT").SaveX(ctx)
	links.Set("project_id", fmt.Sprint(foreign.ID))
	masterPost(t, router, cookie, path+"/links", links, 404)
	limited, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Relationships without master View", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.relations.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, limited); err != nil {
		t.Fatal(err)
	}
	unseen := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("SECRET_UNSEEN_SITE").SaveX(ctx)
	links.Set("project_id", "0")
	links.Set("location_id", fmt.Sprint(unseen.ID))
	masterPost(t, router, cookie, path+"/links", links, 404)
	assertNotContains(t, requestBody(t, router, cookie, "GET", path+"/links", 200), "SECRET_")
}

func TestJobPlanningMultipleContactContextAPIAndScopeLoss(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	a := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Allowed One").SetNotes("SECRET_INTERNAL").SaveX(ctx)
	b := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Allowed Two").SaveX(ctx)
	client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("SECRET_UNLINKED").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned context").SaveX(ctx)
	masterPost(t, router, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/jobs/%d/contacts", j.ID), url.Values{"contact_ids": {fmt.Sprint(a.ID), fmt.Sprint(b.ID)}}, 303)
	u := client.User.Create().SetCompanyID(company).SetEmail("contactfield33@test").SetName("Field").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetUserID(u.ID).SetJobID(j.ID).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, u.ID, "Contact context device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	get := func(method, path string, want int) string {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	path := fmt.Sprintf("/jobs/%d/master-context", j.ID)
	body := get("GET", path, 200)
	assertContains(t, body, "Allowed One")
	assertContains(t, body, "Allowed Two")
	assertNotContains(t, body, "SECRET_")
	get("POST", fmt.Sprintf("/jobs/%d/links", j.ID), 404)
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	get("GET", path, 404)
}

func TestJobPlanningStaleContactFormCannotOverwriteConcurrentLinksHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	a := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("First").SaveX(ctx)
	b := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Second").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Concurrent links").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/contacts", j.ID)
	form := requestBody(t, router, cookie, "GET", path, 200)
	revision := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(form)
	if len(revision) != 2 {
		t.Fatal("missing relationship revision")
	}
	masterPost(t, router, cookie, path, url.Values{"contact_ids": {fmt.Sprint(a.ID)}}, 303)
	conflict := itemPost(t, router, cookie, path, url.Values{"contact_ids": {fmt.Sprint(b.ID)}, "save_action_id": {newOperationKey()}, "save_revision": {revision[1]}}, 409)
	assertContains(t, conflict, "Explicit Job Contacts")
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, `value="`+fmt.Sprint(a.ID)+`" checked`)
	assertNotContains(t, body, `value="`+fmt.Sprint(b.ID)+`" checked`)
}

func TestJobPlanningReopenRequiresExplicitContactRepairHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	a := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Historical contact").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reopen safely").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	masterPost(t, router, cookie, path+"/contacts", url.Values{"contact_ids": {fmt.Sprint(a.ID)}}, 303)
	complete := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("job:completed")).FirstX(ctx)
	start := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("job:new")).FirstX(ctx)
	masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(complete.ID)}}, 303)
	masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/contacts/%d/delete", c.ID, a.ID), nil, 303)
	masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(start.ID)}}, 409)
	masterPost(t, router, cookie, path+"/contacts", url.Values{}, 303)
	masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(start.ID)}}, 303)
}
