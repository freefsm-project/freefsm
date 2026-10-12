package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestNavigationTimeEntryScopesAndFacetsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	reader := client.User.Create().SetCompanyID(company).SetEmail("time-reader@test").SetName("Time reader").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	other := client.User.Create().SetCompanyID(company).SetEmail("time-subject@test").SetName("Other time subject").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE TIME CUSTOMER").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("PRIVATE TIME JOB").SaveX(ctx)
	otherJob := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Outside time assignment").SaveX(ctx)
	client.JobAssignment.Create().SetUserID(reader.ID).SetJobID(job.ID).SaveX(ctx)
	now := time.Now().UTC().Truncate(time.Second)
	linked := client.TimeEntry.Create().SetCompanyID(company).SetUserID(other.ID).SetJobID(job.ID).SetClockIn(now).SetClockOut(now.Add(time.Hour)).SetLatitude(51.234567).SetLongitude(12.345678).SetNotes("Other linked entry").SaveX(ctx)
	unlinked := client.TimeEntry.Create().SetCompanyID(company).SetUserID(other.ID).SetClockIn(now).SetClockOut(now.Add(time.Hour)).SetNotes("Other unlinked entry").SaveX(ctx)
	own := client.TimeEntry.Create().SetCompanyID(company).SetUserID(reader.ID).SetClockIn(now).SetClockOut(now.Add(time.Hour)).SetLatitude(49.987654).SetNotes("Own entry").SaveX(ctx)
	var foreignCompany int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Foreign time company','foreign-time-company') RETURNING id`).Scan(&foreignCompany); err != nil {
		t.Fatal(err)
	}
	foreignUser := client.User.Create().SetCompanyID(foreignCompany).SetEmail("foreign-time@test").SetName("FOREIGN TIME SUBJECT").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	foreignEntry := client.TimeEntry.Create().SetCompanyID(foreignCompany).SetUserID(foreignUser.ID).SetClockIn(now).SetClockOut(now.Add(time.Hour)).SetNotes("FOREIGN TIME ENTRY").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, reader.ID)
	for _, scope := range []authorization.Scope{authorization.All, authorization.Assigned, authorization.Own} {
		t.Run(string(scope), func(t *testing.T) {
			grants := []authorization.Grant{{Permission: "time.view", Scope: scope}}
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Time scope "+string(scope), grants)
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, reader.ID, role); err != nil {
				t.Fatal(err)
			}
			id := linked.ID
			if scope == authorization.Own {
				id = own.ID
			}
			path := fmt.Sprintf("/time-entries/%d", id)
			post := func(values url.Values, want int) {
				t.Helper()
				r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("POST %s = %d want %d: %s", path, w.Code, want, w.Body.String())
				}
			}
			list := requestBody(t, router, cookie, "GET", "/time-entries", 200)
			assertContains(t, list, `href="`+path+`"`)
			assertNotContains(t, list, "FOREIGN TIME")
			expectStatus(t, router, cookie, "GET", fmt.Sprintf("/time-entries/%d", foreignEntry.ID), 404)
			body := requestBody(t, router, cookie, "GET", path, 200)
			for _, hidden := range []string{"PRIVATE TIME JOB", "PRIVATE TIME CUSTOMER", "51.234567", "12.345678", "49.987654", `href="` + path + `/edit"`, `action="` + path + `/delete"`} {
				assertNotContains(t, body, hidden)
			}
			expectStatus(t, router, cookie, "GET", path+"/activity", 200)
			expectStatus(t, router, cookie, "GET", path+"/edit", 403)
			post(url.Values{"notes": {"Unpermitted correction"}}, 403)
			if scope != authorization.All {
				expectStatus(t, router, cookie, "GET", fmt.Sprintf("/time-entries/%d", unlinked.ID), 404)
			}
			if scope == authorization.Assigned {
				expectStatus(t, router, cookie, "GET", fmt.Sprintf("/time-entries/%d", own.ID), 404)
			}
			if scope == authorization.Own {
				dispatcher := client.User.Create().SetCompanyID(company).SetEmail("time-own-dispatcher@test").SetName("Own dispatcher").SetRole("dispatcher").SetPasswordHash("hash").SaveX(ctx)
				if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, dispatcher.ID, role); err != nil {
					t.Fatal(err)
				}
				dispatcherCookie := sessionCookie(t, ctx, sessions, dispatcher.ID)
				expectStatus(t, router, dispatcherCookie, "GET", fmt.Sprintf("/time-entries/%d", linked.ID), 404)
				expectStatus(t, router, dispatcherCookie, "GET", fmt.Sprintf("/time-entries/%d", own.ID), 404)
			}
			grants = append(grants, authorization.Grant{Permission: "time.correct", Scope: scope})
			if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Time correct "+string(scope), grants, 1); err != nil {
				t.Fatal(err)
			}
			body = requestBody(t, router, cookie, "GET", path, 200)
			assertContains(t, body, `href="`+path+`/edit"`)
			form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
			assertNotContains(t, form, "PRIVATE TIME JOB")
			if scope != authorization.Own {
				assertNotContains(t, form, `name="job_id"`)
			}
			r := httptest.NewRequest("POST", path, strings.NewReader(url.Values{"notes": {"Corrected " + string(scope)}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != 303 {
				t.Fatalf("correction = %d: %s", w.Code, w.Body.String())
			}
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Corrected "+string(scope))
			if scope == authorization.All {
				if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_navigation_time_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='time.correct' THEN RAISE EXCEPTION 'injected time audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_navigation_time_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_navigation_time_audit()`); err != nil {
					t.Fatal(err)
				}
				post(url.Values{"notes": {"Must roll back"}}, 409)
				assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Corrected A")
				if _, err := pool.Exec(ctx, `DROP TRIGGER fail_navigation_time_audit ON authorization_audit`); err != nil {
					t.Fatal(err)
				}
			}
			if scope == authorization.Assigned {
				grants = append(grants, authorization.Grant{Permission: "job.view", Scope: authorization.All})
				if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Time correction and Job selection", grants, 2); err != nil {
					t.Fatal(err)
				}
				post(url.Values{"notes": {"Escaped assignment"}, "job_id": {fmt.Sprint(otherJob.ID)}}, 403)
				assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Corrected J")
				client.Job.UpdateOneID(job.ID).SetDeletedAt(time.Now()).SaveX(ctx)
				expectStatus(t, router, cookie, "GET", path, 404)
				client.Job.UpdateOneID(job.ID).ClearDeletedAt().SaveX(ctx)
			}
		})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "All time own GPS", []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.gps.view", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, reader.ID, role); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/time-entries/%d", own.ID), 200), "49.987654")
	assertNotContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/time-entries/%d", linked.ID), 200), "51.234567")
	adminBody := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/time-entries/%d", linked.ID), 200)
	assertContains(t, adminBody, "PRIVATE TIME JOB")
	assertContains(t, adminBody, "51.234567")
	assertContains(t, adminBody, "Other time subject")
}

func TestNavigationTimesheetPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	reader := client.User.Create().SetCompanyID(company).SetEmail("time-browser@test").SetName("Time browser").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser time correction", []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.correct", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, reader.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private time browser Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Private time browser Job").SaveX(ctx)
	now := time.Now().UTC().Truncate(time.Second)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetJobID(job.ID).SetClockIn(now).SetClockOut(now.Add(time.Hour)).SetLatitude(51.234567).SetNotes("Browser other user's entry").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/navigation-time.cjs", server.URL, sessionCookie(t, ctx, sessions, reader.ID).Value, fmt.Sprint(entry.ID), t.TempDir()).CombinedOutput()
	t.Logf("timesheet browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/time-entries/%d", entry.ID), 200)
	assertContains(t, body, "Persisted time correction")
	assertContains(t, body, "Private time browser Job")
	assertContains(t, body, "51.234567")
}
