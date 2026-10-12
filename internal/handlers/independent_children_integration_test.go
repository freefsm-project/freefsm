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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justinas/nosurf"
)

func childOnlyGrants(kind string) []authorization.Grant {
	var grants []authorization.Grant
	for _, action := range []string{"view", "create", "edit", "archive", "archived.view", "restore"} {
		grants = append(grants, authorization.Grant{Permission: kind + "." + action, Scope: authorization.All})
	}
	return grants
}

func queuedChildPost(t *testing.T, pool *pgxpool.Pool, router http.Handler, cookie *http.Cookie, path, change string, args []any, want int, submitted ...url.Values) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, change, args...); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	values := url.Values{"service_notes": {"After fence"}}
	if len(submitted) > 0 {
		values = submitted[0]
	}
	go func() {
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode())).WithContext(ctx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w
	}()
	for {
		var waiting bool
		if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != want {
		t.Fatalf("queued child command: %d want %d: %s", w.Code, want, w.Body.String())
	}
}

func TestIndependentChildCommitAuthorizationAndLifecycleHTTP(t *testing.T) {
	for _, kind := range []string{"contact", "location"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			svc := authorization.New(pool)
			grants := append(childOnlyGrants(kind), authorization.Grant{Permission: "customer.view", Scope: authorization.All})
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Waiting child editor", grants)
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetEmail("fence@test").SetName("Editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("SECRET_CUSTOMER").SaveX(ctx)
			var id int64
			if kind == "contact" {
				id = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Person").SaveX(ctx).ID
			} else {
				id = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Place").SaveX(ctx).ID
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			path := fmt.Sprintf("/%ss/%d", kind, id)
			queuedChildPost(t, pool, router, cookie, path, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.view'`, []any{role}, 303)
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "After fence")
			if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Selectable Customer", grants, 1); err != nil {
				t.Fatal(err)
			}
			creation := url.Values{"first_name": {"New person"}}
			if kind == "location" {
				creation = url.Values{"title": {"New place"}}
			}
			queuedChildPost(t, pool, router, cookie, fmt.Sprintf("/customers/%d/%ss", c.ID, kind), `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.view'`, []any{role}, 404, creation)
			queuedChildPost(t, pool, router, cookie, path, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, []any{role, kind + ".edit"}, 403)
			if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Child grants retained", childOnlyGrants(kind), 2); err != nil {
				t.Fatal(err)
			}
			if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_independent_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type IN ('contact','location') THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$;CREATE TRIGGER fail_independent_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_independent_audit()`); err != nil {
				t.Fatal(err)
			}
			masterPost(t, router, cookie, path, url.Values{"service_notes": {"Must roll back"}}, 409)
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "After fence")
			if _, err = pool.Exec(ctx, `DROP TRIGGER fail_independent_audit ON authorization_audit`); err != nil {
				t.Fatal(err)
			}
			queuedChildPost(t, pool, router, cookie, path, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, []any{c.ID}, 403)
			body := requestBody(t, router, cookie, "GET", path, 200)
			assertNotContains(t, body, "SECRET_")
			assertNotContains(t, body, `/edit"`)
			var count int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type=$1 AND target_id=$2`, kind, id).Scan(&count); err != nil || count != 1 {
				t.Fatalf("expected one atomic accepted edit: %d %v", count, err)
			}
		})
	}
}

func TestIndependentChildrenPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent child browser", append(childOnlyGrants("contact"), childOnlyGrants("location")...))
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("independent-browser@test").SetName("Child operator").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("SECRET_CUSTOMER").SetBillingCity("SECRET_BILLING").SaveX(ctx)
	client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Browser person").SaveX(ctx)
	client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Browser place").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/independent-children.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}

func TestIndependentChildMasterHTTP(t *testing.T) {
	for _, kind := range []string{"contact", "location"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			svc := authorization.New(pool)
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Child only", childOnlyGrants(kind))
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetEmail("child-only@test").SetName("Child operator").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("SECRET_CUSTOMER").SetNotes("SECRET_NOTES").SetBillingCity("SECRET_BILLING").SaveX(ctx)
			var id int64
			fields := url.Values{"first_name": {"Independent person"}}
			if kind == "contact" {
				id = client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("Independent person").SaveX(ctx).ID
			} else {
				id = client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Independent place").SaveX(ctx).ID
				fields = url.Values{"title": {"Independent place"}}
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			base := fmt.Sprintf("/customers/%d/%ss", c.ID, kind)
			path := fmt.Sprintf("%s/%d", base, id)
			expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/not-an-id/%ss/%d", kind, id), 404)
			for _, route := range []string{path, path + "/edit", base, base + "/options"} {
				body := requestBody(t, router, cookie, "GET", route, 200)
				assertNotContains(t, body, "SECRET_")
				assertNotContains(t, body, "Back to Customer")
			}
			r := httptest.NewRequest("GET", path, nil)
			r.AddCookie(cookie)
			r.Header.Set("Accept", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != 200 || strings.Contains(w.Body.String(), "customer_id") {
				t.Fatalf("child representation leaked parent: %d %s", w.Code, w.Body.String())
			}
			fields.Set("service_notes", "Child-only edit")
			redirect := masterPost(t, router, cookie, path, fields, 303)
			if redirect != fmt.Sprintf("/%ss/%d", kind, id) {
				t.Fatalf("response links hidden Customer: %s", redirect)
			}
			for _, route := range []string{fmt.Sprintf("/%ss", kind), redirect, redirect + "/edit", fmt.Sprintf("/%ss/options", kind)} {
				body := requestBody(t, router, cookie, "GET", route, 200)
				assertNotContains(t, body, fmt.Sprintf("/customers/%d", c.ID))
				assertNotContains(t, body, "SECRET_")
			}
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Child-only edit")
			masterPost(t, router, cookie, base, fields, 404) // A selected Customer is an independently authorized input.
			masterPost(t, router, cookie, base+"/inline", fields, 404)
			expectStatus(t, router, cookie, "GET", base+"/new", 404)
			expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 404)
			masterPost(t, router, cookie, path+"/delete", nil, 303)
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Archived")
			options := requestBody(t, router, cookie, "GET", fmt.Sprintf("/%ss/options?archived=1", kind), 200)
			assertNotContains(t, options, "Independent")
			masterPost(t, router, cookie, path+"/restore", nil, 303)
			// A guessed parent URL cannot turn a foreign child into an editable target.
			masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/%ss/%d", c.ID+100, kind, id), fields, 404)
			expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/%ss/options", c.ID+100, kind), 200)
			if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Independent reader", []authorization.Grant{{Permission: kind + ".view", Scope: authorization.All}}, 1); err != nil {
				t.Fatal(err)
			}
			expectStatus(t, router, cookie, "GET", path, 200)
			expectStatus(t, router, cookie, "GET", path+"/edit", 403)
			masterPost(t, router, cookie, path, fields, 403)
			masterPost(t, router, cookie, path+"/delete", nil, 403)
			if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Closed context only", []authorization.Grant{{Permission: kind + ".view", Scope: authorization.Context}, {Permission: "job.view", Scope: authorization.Assigned}}, 2); err != nil {
				t.Fatal(err)
			}
			for _, route := range []string{path, fmt.Sprintf("/%ss", kind), fmt.Sprintf("/%ss/options", kind)} {
				expectStatus(t, router, cookie, "GET", route, 404)
			}
		})
	}
}
