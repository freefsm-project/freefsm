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

func jobDocumentNavigationGrants(kind string, scope authorization.Scope) []authorization.Grant {
	opposite := "estimate"
	if kind == "estimate" {
		opposite = "invoice"
	}
	return []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "pricing.view", Scope: authorization.All}, {Permission: kind + ".view", Scope: scope}, {Permission: opposite + ".view", Scope: authorization.All}, {Permission: kind + ".create", Scope: scope}}
}

func TestNavigationJobDocumentCreationIndependentGrantsHTTP(t *testing.T) {
	for _, kind := range []string{"invoice", "estimate"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			svc := authorization.New(pool)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Job document Customer").SaveX(ctx)
			other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other document Customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unassigned document source").SetLineItems(`[{"title":"Captured work","quantity":1,"unit_price":77}]`).SaveX(ctx)
			base := fmt.Sprintf("/jobs/%d", job.ID)
			grants := jobDocumentNavigationGrants(kind, authorization.All)
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Specific "+kind+" creator", grants)
			if err != nil {
				t.Fatal(err)
			}
			var techID int64
			for _, legacy := range []string{"tech", "dispatcher"} {
				u := client.User.Create().SetCompanyID(company).SetEmail(kind + legacy + "-navigation@test").SetName("Document creator").SetPasswordHash("hash").SetRole(legacy).SaveX(ctx)
				if legacy == "tech" {
					techID = u.ID
				}
				if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
					t.Fatal(err)
				}
				cookie := sessionCookie(t, ctx, sessions, u.ID)
				body := requestBody(t, router, cookie, "GET", base, 200)
				assertContains(t, body, `action="`+base+`/create-`+kind+`"`)
				opposite := "estimate"
				if kind == "estimate" {
					opposite = "invoice"
				}
				assertNotContains(t, body, `action="`+base+`/create-`+opposite+`"`)
				expectStatus(t, router, cookie, "POST", base+"/create-"+opposite, 403)
				path := base + "/create-estimate"
				values := url.Values{}
				if kind == "invoice" {
					prepared := itemPost(t, router, cookie, base+"/create-invoice", url.Values{}, 200)
					assertContains(t, prepared, `action="`+base+`/invoices"`)
					assertContains(t, prepared, "Captured work")
					expectStatus(t, router, cookie, "GET", base+"/create-invoice", 200)
					path = base + "/invoices"
					values = url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Saved " + kind + " " + legacy}, "tax_rate": {"0"}, "line_items": {`[{"title":"Captured work","quantity":1,"unit_price":77}]`}}
				}
				r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if w.Code != 303 {
					t.Fatalf("%s create = %d: %s", legacy, w.Code, w.Body.String())
				}
				location := w.Header().Get("Location")
				if !strings.HasPrefix(location, "/"+kind+"s/") {
					t.Fatalf("unexpected saved destination %q", location)
				}
				assertContains(t, requestBody(t, router, cookie, "GET", location, 200), "Captured work")
				assertContains(t, requestBody(t, router, cookie, "GET", location, 200), "$77.00")
			}
			cookie := sessionCookie(t, ctx, sessions, techID)
			bad := url.Values{"customer_id": {fmt.Sprint(other.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Cross Customer must reject"}, "tax_rate": {"0"}, "line_items": {"[]"}}
			path := "/estimates"
			if kind == "invoice" {
				path = base + "/invoices"
			}
			itemPost(t, router, cookie, path, bad, 409)
			assertNotContains(t, requestBody(t, router, cookie, "GET", "/"+kind+"s", 200), "Cross Customer must reject")
			if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Assigned "+kind+" creation", jobDocumentNavigationGrants(kind, authorization.Assigned), 1); err != nil {
				t.Fatal(err)
			}
			assertNotContains(t, requestBody(t, router, cookie, "GET", base, 200), `action="`+base+`/create-`+kind+`"`)
			expectStatus(t, router, cookie, "POST", base+"/create-"+kind, 403)
			client.JobAssignment.Create().SetJobID(job.ID).SetUserID(techID).SaveX(ctx)
			assertContains(t, requestBody(t, router, cookie, "GET", base, 200), `action="`+base+`/create-`+kind+`"`)
			if kind == "invoice" {
				expectStatus(t, router, cookie, "GET", base+"/create-invoice", 200)
				itemPost(t, router, cookie, base+"/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Assigned Invoice save"}, "tax_rate": {"0"}, "line_items": {"[]"}}, 303)
			} else {
				expectStatus(t, router, cookie, "POST", base+"/create-estimate", 303)
			}
			// A document Create grant cannot replace independent Job View, even
			// when the actor happens to be assigned to the source Job.
			var withoutJobView []authorization.Grant
			for _, grant := range jobDocumentNavigationGrants(kind, authorization.All) {
				if grant.Permission != "job.view" {
					withoutJobView = append(withoutJobView, grant)
				}
			}
			if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Document create without Job View", withoutJobView, 2); err != nil {
				t.Fatal(err)
			}
			expectStatus(t, router, cookie, "POST", base+"/create-"+kind, 403)
		})
	}
}

func TestNavigationJobDocumentPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	grants := jobDocumentNavigationGrants("invoice", authorization.All)
	grants = append(grants, authorization.Grant{Permission: "estimate.create", Scope: authorization.All})
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Both document creators", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("job-document-browser@test").SetName("Unassigned creator").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser document Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser document source").SetLineItems(`[{"title":"Browser captured line","quantity":1,"unit_price":77}]`).SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/navigation-job-documents.cjs", server.URL, sessionCookie(t, ctx, sessions, u.ID).Value, fmt.Sprint(job.ID), t.TempDir()).CombinedOutput()
	t.Logf("Job document browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNavigationJobDocumentGrantLossAtCommitHTTP(t *testing.T) {
	for _, kind := range []string{"invoice", "estimate"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			svc := authorization.New(pool)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Commit fence Customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("No document after grant loss").SaveX(ctx)
			grants := jobDocumentNavigationGrants(kind, authorization.All)
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Commit-fenced "+kind, grants)
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetEmail(kind + "-commit-navigation@test").SetName("Revoked creator").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			base := fmt.Sprintf("/jobs/%d", job.ID)
			assertContains(t, requestBody(t, router, cookie, "GET", base, 200), `action="`+base+`/create-`+kind+`"`)
			if kind == "invoice" {
				expectStatus(t, router, cookie, "GET", base+"/create-invoice", 200)
			}
			hold, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer hold.Rollback(ctx)
			if _, err := hold.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			waitFor := func(count int) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for time.Now().Before(deadline) {
					var waiting int
					if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND wait_event='advisory'`, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting >= count {
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
				t.Fatalf("did not observe %d authority-fence waiters", count)
			}
			revoked := make(chan error, 1)
			var remaining []authorization.Grant
			for _, grant := range grants {
				if grant.Permission != kind+".create" {
					remaining = append(remaining, grant)
				}
			}
			go func() {
				_, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Revoked "+kind+" creation", remaining, 1)
				revoked <- err
			}()
			waitFor(1)
			response := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				path := base + "/create-estimate"
				values := url.Values{}
				if kind == "invoice" {
					path = base + "/invoices"
					values = url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"No document after grant loss"}, "tax_rate": {"0"}, "line_items": {"[]"}}
				}
				r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				response <- w
			}()
			waitFor(2)
			if err := hold.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-revoked; err != nil {
				t.Fatal(err)
			}
			w := <-response
			if w.Code < 400 {
				t.Fatalf("grant loss committed %d: %s", w.Code, w.Body.String())
			}
			assertNotContains(t, requestBody(t, router, cookie, "GET", "/"+kind+"s", 200), "No document after grant loss")
			assertNotContains(t, requestBody(t, router, cookie, "GET", base, 200), `action="`+base+`/create-`+kind+`"`)
		})
	}
}
