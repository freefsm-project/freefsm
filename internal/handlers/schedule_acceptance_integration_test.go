package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/jackc/pgx/v5"
)

func TestScheduleIntersectingAndBacklogHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Schedule owner").SaveX(ctx)
	start := time.Date(2026, 10, 19, 23, 30, 0, 0, time.UTC)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Cross midnight unassigned").SetStartTime(start).SetEndTime(start.Add(2 * time.Hour)).SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Undated backlog").SaveX(ctx)
	for _, tab := range []string{"list", "calendar", "dispatch", "map"} {
		t.Run(tab, func(t *testing.T) {
			body := requestBody(t, router, cookie, "GET", "/schedule?tab="+tab+"&period=day&date=2026-10-20", 200)
			assertContains(t, body, "Cross midnight unassigned")
			assertContains(t, body, "Unassigned")
			if tab == "list" || tab == "dispatch" {
				assertContains(t, body, "Undated backlog")
			}
		})
	}
	// The interval starts before every requested calendar range. It must appear
	// on the actual intersected day rather than the original start-day number.
	previousMonth := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	carried := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Carried from previous month").SetStartTime(previousMonth).SetEndTime(time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(carried.ID).SetUserID(admin).SaveX(ctx)
	for _, tab := range []string{"list", "calendar", "dispatch", "map"} {
		for _, period := range []string{"week", "month"} {
			body := requestBody(t, router, cookie, "GET", "/schedule?tab="+tab+"&period="+period+"&date=2026-10-03", 200)
			assertContains(t, body, "Carried from previous month")
			if tab == "dispatch" {
				parts := strings.SplitN(body, `<table class="dispatch-matrix">`, 2)
				if len(parts) != 2 {
					t.Fatal("missing dispatch matrix")
				}
				assertContains(t, parts[1], "Carried from previous month")
			}
		}
	}
}

func TestScheduleAuthorityRaceHTTP(t *testing.T) {
	for _, kind := range []string{"assignment", "grant"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			auth := authorization.New(pool)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Racing reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}})
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetName("Reader").SetEmail("race37@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
			if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race owner").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Revoked schedule secret").SaveX(ctx)
			client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var pid int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) fence`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("GET", "/schedule?tab=list&period=day&date=2026-10-20", nil)
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w
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
					t.Fatal("schedule did not wait for current authority")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if kind == "assignment" {
				_, err = tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID)
			} else {
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1`, role)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w := <-done
			if kind == "grant" && w.Code != 403 {
				t.Fatalf("grant loss: %d %s", w.Code, w.Body.String())
			}
			if kind == "assignment" && w.Code != 200 {
				t.Fatalf("assignment loss: %d", w.Code)
			}
			assertNotContains(t, w.Body.String(), "Revoked schedule secret")
		})
	}
}

func TestScheduleReadFailureIsNotEmptyHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Failure owner").SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Previously visible schedule").SaveX(ctx)
	assertContains(t, requestBody(t, router, cookie, "GET", "/schedule?tab=list&period=day", 200), "Previously visible schedule")
	if _, err := pool.Exec(ctx, `ALTER TABLE jobs RENAME COLUMN end_time TO injected_unavailable_end`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `ALTER TABLE jobs RENAME COLUMN injected_unavailable_end TO end_time`)
	body := requestBody(t, router, cookie, "GET", "/schedule?tab=list&period=day&q=retained", 503)
	assertContains(t, body, "Schedule unavailable")
	assertContains(t, body, `value="retained"`)
	assertNotContains(t, body, "Previously visible schedule")
	assertNotContains(t, body, "No Jobs")
}

func TestScheduleIncompleteIntervalRepairHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Repair owner").SaveX(ctx)
	start := time.Date(2020, 1, 1, 9, 0, 0, 0, time.UTC)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Incomplete recorded interval").SetStartTime(start).SaveX(ctx)
	for _, tab := range []string{"list", "calendar", "dispatch", "map"} {
		t.Run(tab, func(t *testing.T) {
			body := requestBody(t, router, cookie, "GET", "/schedule?tab="+tab+"&period=day&date=2026-10-20", 200)
			assertContains(t, body, "Incomplete recorded interval")
			assertContains(t, body, "reviewed repair")
		})
	}
	got := client.Job.GetX(ctx, j.ID)
	if got.EndTime != nil || got.StartTime == nil || !got.StartTime.Equal(start) {
		t.Fatal("schedule read invented or changed interval")
	}
}

func TestSchedulePersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser schedule reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: "location.view", Scope: authorization.Context}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Browser schedule worker").SetEmail("browser37@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser schedule owner").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Zero coordinate service site").SetAddress1("37 Browser Road").SetLatitude(0).SetLongitude(0).SaveX(ctx)
	start := time.Date(2026, 10, 19, 23, 30, 0, 0, time.UTC)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(l.ID).SetJobType("Browser crossing Job").SetStartTime(start).SetEndTime(start.Add(2 * time.Hour)).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	unmapped := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser unmapped Job").SetStartTime(start).SetEndTime(start.Add(2 * time.Hour)).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(unmapped.ID).SetUserID(u.ID).SaveX(ctx)
	if _, err = pool.Exec(ctx, `UPDATE company_settings SET map_tile_url='https://tiles.example.test/{z}/{x}/{y}.png' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.HandleFunc("/test/reassign", func(w http.ResponseWriter, r *http.Request) {
		err := auth.WithinActor(r.Context(), authorization.Actor{UserID: admin}, func(tx pgx.Tx, p authorization.Principal) error {
			_, err := tx.Exec(r.Context(), `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID)
			return err
		})
		if err != nil {
			http.Error(w, "fixture reassignment failed", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/schedule.cjs", server.URL, cookie.Value, fmt.Sprint(j.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	got := client.Location.GetX(context.Background(), l.ID)
	if got.Latitude == nil || got.Longitude == nil || *got.Latitude != 0 || *got.Longitude != 0 {
		t.Fatal("provider failure changed coordinates")
	}
}

func TestScheduleFiltersAndReturnStateHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Filter owner").SaveX(ctx)
	var finished int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' LIMIT 1`, company).Scan(&finished); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Zulu finished").SetStatusID(finished).SetStartTime(start).SetEndTime(start.Add(time.Hour)).SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Alpha finished").SetStatusID(finished).SetStartTime(start.Add(time.Hour)).SetEndTime(start.Add(2 * time.Hour)).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned open work").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	r := httptest.NewRequest("GET", "/schedule?tab=list&period=day&date=2026-10-20&status=finished&assignment=unassigned&sort=title", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("filters: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	assertContains(t, body, "Alpha finished")
	assertContains(t, body, "Zulu finished")
	assertNotContains(t, body, "Assigned open work")
	if strings.Index(body, "Alpha finished") > strings.Index(body, "Zulu finished") {
		t.Fatal("title order ignored")
	}
	returned := httptest.NewRequest("GET", "/schedule", nil)
	returned.AddCookie(cookie)
	for _, pref := range w.Result().Cookies() {
		returned.AddCookie(pref)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, returned)
	if w.Code != 200 {
		t.Fatalf("return: %d", w.Code)
	}
	assertContains(t, w.Body.String(), "Alpha finished")
	assertNotContains(t, w.Body.String(), "Assigned open work")
	assertContains(t, requestBody(t, router, cookie, "GET", "/schedule?tab=list&period=day&date=2026-10-20&q=unmatched", 200), "No matching Jobs")
}

func TestScheduleResponsiveDefaultPreservesPreferencesHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE users SET last_schedule_tab='map',last_schedule_period='week' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", "/schedule?tab=list&period=day&responsive=1", 200)
	var tab, period string
	if err := pool.QueryRow(ctx, `SELECT last_schedule_tab,last_schedule_period FROM users WHERE id=$1`, admin).Scan(&tab, &period); err != nil {
		t.Fatal(err)
	}
	if tab != "map" || period != "week" {
		t.Fatalf("responsive fallback rewrote recorded preferences: %s/%s", tab, period)
	}
}

func TestScheduleRetainsAuthorizedInvoiceBadgeHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Invoice badge owner").SaveX(ctx)
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Invoice badge work").SetStartTime(start).SetEndTime(start.Add(time.Hour)).SaveX(ctx)
	invoice := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Independently authorized Invoice").SetInvoiceNumber(1).SetInvoiceDate(start).SetDueDate(start.AddDate(0, 0, 7)).SaveX(ctx)
	path := "/schedule?tab=list&period=day&date=2026-10-20"
	link := fmt.Sprintf(`href="/invoices/%d"`, invoice.ID)
	assertContains(t, requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", path, 200), link)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent invoice reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Independent reader").SetEmail("invoice37@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	assertNotContains(t, requestBody(t, router, cookie, "GET", path, 200), link)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), link)
}

func TestScheduleAuthorizedCardContextHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Schedule context", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: "location.view", Scope: authorization.Context}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Equal worker").SetEmail("schedule37@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Bounded context owner").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Service site").SetAddress1("37 Service Road").SetLatitude(0).SetLongitude(0).SaveX(ctx)
	start := time.Date(2026, 10, 19, 23, 30, 0, 0, time.UTC)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(l.ID).SetJobType("Authorized card").SetStartTime(start).SetEndTime(start.Add(2 * time.Hour)).SetArrivalWindowStart(start.Add(-time.Hour)).SetArrivalWindowEnd(start).SetDueDate(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	secret := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Hidden overlap secret").SetStartTime(start).SetEndTime(start.Add(time.Hour)).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(secret.ID).SetUserID(admin).SaveX(ctx)
	for _, tab := range []string{"list", "calendar", "dispatch", "map"} {
		t.Run(tab, func(t *testing.T) {
			body := requestBody(t, router, cookie, "GET", "/schedule?tab="+tab+"&period=day&date=2026-10-20", 200)
			for _, text := range []string{"Authorized card", "Bounded context owner", "37 Service Road", "Equal worker", "Admin", "Arrival", "Overdue", "2026-10-19", "2026-10-20"} {
				assertContains(t, body, text)
			}
			assertNotContains(t, body, "Hidden overlap secret")
			assertNotContains(t, body, "Schedule conflict")
			if tab == "map" {
				assertContains(t, body, `data-lat="0.000000"`)
				assertNotContains(t, body, "No map coordinates yet")
			}
		})
	}
}
