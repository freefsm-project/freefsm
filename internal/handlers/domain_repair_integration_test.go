package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/status"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestDomainRepairIndependentLifecycleHTTP(t *testing.T) {
	for _, typ := range []string{"project", "asset"} {
		t.Run(typ, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("SECRET owner").SaveX(ctx)
			loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("SECRET site").SaveX(ctx)
			var id int64
			if typ == "project" {
				completed := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("project:completed")).FirstX(ctx)
				id = client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetStatusID(completed.ID).SetName("Lifecycle project").SaveX(ctx).ID
			} else {
				at := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
				id = client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetOwnershipClass("customer_owned").SetName("Lifecycle asset").SetAssetTypeID(at.ID).SaveX(ctx).ID
			}
			svc := authorization.New(pool)
			grants := []authorization.Grant{{Permission: typ + ".view", Scope: authorization.All}, {Permission: typ + ".archived.view", Scope: authorization.All}, {Permission: typ + ".archive", Scope: authorization.All}, {Permission: typ + ".restore", Scope: authorization.All}}
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent lifecycle", grants)
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetEmail("lifecycle@test").SetName("Lifecycle user").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			path := fmt.Sprintf("/%ss/%d", typ, id)
			body := requestBody(t, router, cookie, "GET", path, 200)
			assertContains(t, body, path+"/delete")
			masterPost(t, router, cookie, path+"/delete", nil, 303)
			body = requestBody(t, router, cookie, "GET", path, 200)
			assertContains(t, body, path+"/restore")
			assertNotContains(t, body, "SECRET")
			masterPost(t, router, cookie, path+"/restore", nil, 303)
			masterPost(t, router, cookie, path+"/delete", nil, 303)
			// A render is advisory: current dependencies and authority still gate POST.
			masterPost(t, router, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/customers/%d/locations/%d/delete", c.ID, loc.ID), nil, 303)
			if typ == "asset" {
				for _, viewer := range []*http.Cookie{cookie, sessionCookie(t, ctx, sessions, admin)} {
					body = requestBody(t, router, viewer, "GET", path, 200)
					assertContains(t, body, "Archived on")
					assertNotContains(t, body, path+"/restore")
				}
				masterPost(t, router, cookie, path+"/restore", nil, 409)
			} else {
				// A finished Project may restore with its historical archived Location.
				assertContains(t, requestBody(t, router, cookie, "GET", path, 200), path+"/restore")
				masterPost(t, router, cookie, path+"/restore", nil, 303)
				masterPost(t, router, cookie, path+"/delete", nil, 303)
			}
			client.Customer.UpdateOneID(c.ID).SetDeletedAt(time.Now()).SaveX(ctx)
			for _, viewer := range []*http.Cookie{cookie, sessionCookie(t, ctx, sessions, admin)} {
				body = requestBody(t, router, viewer, "GET", path, 200)
				assertContains(t, body, "Archived on")
				assertNotContains(t, body, path+"/restore")
			}
			masterPost(t, router, cookie, path+"/restore", nil, 409)
			client.Customer.UpdateOneID(c.ID).ClearDeletedAt().SaveX(ctx)
			if _, err = pool.Exec(ctx, `UPDATE locations SET deleted_at=NULL WHERE id=$1`, loc.ID); err != nil {
				t.Fatal(err)
			}
			// Archived visibility remains an independent prerequisite.
			if _, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, typ+".archived.view"); err != nil {
				t.Fatal(err)
			}
			masterPost(t, router, cookie, path+"/restore", nil, 404)
		})
	}
}

func TestDomainRepairRetainedLocationHTTP(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("permissionHidden=%v", hidden), func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
			loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("SECRET retained site").SaveX(ctx)
			jobLoc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Independent Job site").SaveX(ctx)
			completed := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("project:completed")).FirstX(ctx)
			if hidden {
				completed = client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("project:new")).FirstX(ctx)
			}
			p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetStatusID(completed.ID).SetName("Retained site").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetLocationID(jobLoc.ID).SetJobType("Independent").SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, admin)
			if hidden {
				svc := authorization.New(pool)
				role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Project editor", []authorization.Grant{{Permission: "project.view", Scope: authorization.All}, {Permission: "project.edit", Scope: authorization.All}})
				if err != nil {
					t.Fatal(err)
				}
				u := client.User.Create().SetCompanyID(company).SetEmail("retained@test").SetName("Editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
				if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
					t.Fatal(err)
				}
				cookie = sessionCookie(t, ctx, sessions, u.ID)
			} else {
				masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/locations/%d/delete", c.ID, loc.ID), nil, 303)
			}
			path := fmt.Sprintf("/projects/%d", p.ID)
			body := requestBody(t, router, cookie, "GET", path+"/edit", 200)
			assertContains(t, body, `value="keep" selected`)
			assertContains(t, body, "Keep current unavailable Location")
			assertNotContains(t, body, "SECRET retained site")
			values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {"Unrelated edit"}, "location_id": {"keep"}, "service_notes": {"Changed service notes"}}
			// A validation rerender must retain the same keep intent.
			values.Set("name", "")
			r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("validation rerender: %d %s", w.Code, w.Body.String())
			}
			body = w.Body.String()
			assertContains(t, body, `value="keep" selected`)
			values.Set("name", "Unrelated edit")
			masterPost(t, router, cookie, path, values, 303)
			got := client.Project.GetX(ctx, p.ID)
			if got.LocationID == nil || *got.LocationID != loc.ID || got.ServiceNotes != "Changed service notes" {
				t.Fatal("unrelated edit lost retained location or notes")
			}
			values.Set("location_id", "0")
			masterPost(t, router, cookie, path, values, 303)
			if client.Project.GetX(ctx, p.ID).LocationID != nil {
				t.Fatal("explicit clear was not applied")
			}
			if got := client.Job.GetX(ctx, j.ID); got.LocationID == nil || *got.LocationID != jobLoc.ID {
				t.Fatal("Project edit changed independent Job Location")
			}
		})
	}
}

func TestDomainRepairWorkflowReopeningHTTP(t *testing.T) {
	for _, archived := range []bool{false, true} {
		t.Run(fmt.Sprintf("archived=%v", archived), func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			masterPost(t, router, cookie, "/settings/statuses/project", url.Values{"name": {"Custom finished"}, "category": {"project:completed"}}, 303)
			custom := client.Status.Query().Where(status.CompanyIDEQ(company), status.NameEQ("Custom finished")).OnlyX(ctx)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
			loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Retained site").SaveX(ctx)
			p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetStatusID(custom.ID).SetCompletionPercentage(37).SetName("Finished project").SaveX(ctx)
			path := fmt.Sprintf("/projects/%d", p.ID)
			if archived {
				masterPost(t, router, cookie, path+"/delete", nil, 303)
			}
			masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/locations/%d/delete", c.ID, loc.ID), nil, 303)
			move := fmt.Sprintf("/settings/statuses/project/%d/move", custom.ID)
			values := url.Values{"category": {"project:in_progress"}, "order": {"1"}, "confirm_in_use": {"true"}}
			masterPost(t, router, cookie, move, values, 409)
			if got := client.Status.GetX(ctx, custom.ID); got.CategoryKey != custom.CategoryKey || got.CategoryOrder != custom.CategoryOrder {
				t.Fatal("rejected move changed status")
			}
			var audits int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='configuration.workflow.manage' AND detail->>'action'='status_moved'`).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			if audits != 0 {
				t.Fatal("rejected move published audit")
			}
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "100%")
			// Same-category reorder and a finished-to-finished move remain valid.
			values.Set("category", "project:completed")
			masterPost(t, router, cookie, move, values, 303)
			values.Set("category", "project:canceled")
			masterPost(t, router, cookie, move, values, 303)
			values.Set("category", "project:in_progress")
			masterPost(t, router, cookie, move, values, 409)
			if archived {
				masterPost(t, router, cookie, path+"/restore", nil, 303)
			}
			masterPost(t, router, cookie, path, url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "location_id": {"0"}}, 303)
			masterPost(t, router, cookie, move, values, 303)
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "37%")
		})
	}
}

func TestDomainRepairWorkflowMoveRechecksDependenciesAndAuditHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	masterPost(t, router, cookie, "/settings/statuses/project", url.Values{"name": {"Race finished"}, "category": {"project:completed"}}, 303)
	custom := client.Status.Query().Where(status.CompanyIDEQ(company), status.NameEQ("Race finished")).OnlyX(ctx)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Race site").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetStatusID(custom.ID).SetName("Racing project").SaveX(ctx)
	client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(custom.ID).SetName("Other affected project").SaveX(ctx)
	move := fmt.Sprintf("/settings/statuses/project/%d/move", custom.ID)
	values := url.Values{"category": {"project:in_progress"}, "confirm_in_use": {"true"}}
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
		r := httptest.NewRequest("POST", move, strings.NewReader(values.Encode()))
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
			t.Fatal("workflow move did not reach committing fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = tx.Exec(ctx, `UPDATE locations SET deleted_at=now() WHERE id=$1`, loc.ID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 409 {
		t.Fatalf("racing dependency move returned %d", code)
	}
	if got := client.Status.GetX(ctx, custom.ID); got.CategoryKey != "project:completed" {
		t.Fatal("racing move reopened affected Projects")
	}
	masterPost(t, router, cookie, fmt.Sprintf("/projects/%d", p.ID), url.Values{"customer_id": {fmt.Sprint(c.ID)}, "name": {p.Name}, "location_id": {"0"}}, 303)
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_reopening_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='configuration.workflow.manage' AND NEW.detail->>'action'='status_moved' THEN RAISE EXCEPTION 'injected workflow audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_reopening_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_reopening_audit()`); err != nil {
		t.Fatal(err)
	}
	// Workflow command errors use the existing 422 response contract.
	masterPost(t, router, cookie, move, values, 422)
	if got := client.Status.GetX(ctx, custom.ID); got.CategoryKey != "project:completed" || got.CategoryOrder != custom.CategoryOrder {
		t.Fatal("audit failure partially committed semantic move")
	}
	var activities int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE action='status_moved' AND object_type='status' AND object_id=$1`, custom.ID).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if activities != 0 {
		t.Fatal("audit failure left semantic move activity")
	}
}

func TestDomainRepairPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser owner").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("SECRET retained site").SaveX(ctx)
	jobLoc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Independent Job site").SaveX(ctx)
	completed := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ("project:completed")).FirstX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetStatusID(completed.ID).SetName("Browser lifecycle project").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetLocationID(jobLoc.ID).SetJobType("Independent").SaveX(ctx)
	at := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Browser lifecycle asset").SetAssetTypeID(at.ID).SaveX(ctx)
	svc := authorization.New(pool)
	var grants []authorization.Grant
	for _, typ := range []string{"project", "asset"} {
		for _, action := range []string{"view", "edit", "archive", "archived.view", "restore"} {
			grants = append(grants, authorization.Grant{Permission: typ + "." + action, Scope: authorization.All})
		}
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser independent lifecycle", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("browserlifecycle@test").SetName("Browser user").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	for _, phase := range []string{"hidden", "archived", "clear"} {
		cookie := sessionCookie(t, ctx, sessions, u.ID)
		if phase == "archived" {
			cookie = sessionCookie(t, ctx, sessions, admin)
			masterPost(t, router, cookie, fmt.Sprintf("/customers/%d/locations/%d/delete", c.ID, loc.ID), nil, 303)
		}
		browserCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		script, err := filepath.Abs("testdata/browser/domain-repair.cjs")
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(browserCtx, "node", script, server.URL, cookie.Value, fmt.Sprint(p.ID), fmt.Sprint(a.ID), phase)
		// A short procfs alias avoids Chromium's Unix socket path limit while
		// keeping all browser artifacts in the caller-owned temporary directory.
		command.Dir = t.TempDir()
		if runtime.GOOS == "linux" {
			command.Env = append(os.Environ(), "TMPDIR=/proc/self/cwd")
		}
		out, err := command.CombinedOutput()
		cancel()
		t.Logf("browser %s: %s", phase, out)
		if err != nil {
			t.Fatal(err)
		}
		got := client.Project.GetX(ctx, p.ID)
		if phase == "clear" {
			if got.LocationID != nil {
				t.Fatal("browser clear not persisted")
			}
		} else if got.LocationID == nil || *got.LocationID != loc.ID {
			t.Fatal("browser unrelated save lost retained Location")
		}
		if got := client.Job.GetX(ctx, j.ID); got.LocationID == nil || *got.LocationID != jobLoc.ID {
			t.Fatal("browser changed independent Job Location")
		}
		if got.DeletedAt != nil || client.Asset.GetX(ctx, a.ID).DeletedAt != nil {
			t.Fatal("browser lifecycle did not persist restore")
		}
	}
}
