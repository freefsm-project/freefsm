package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/jobassignment"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestJobDispatchReviewedAtomicHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Dispatch owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reviewed work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/dispatch", j.ID)
	requestBody(t, router, cookie, "GET", path, 200)
	input := url.Values{"start": {"2026-10-14T23:15"}, "end": {"2026-10-15T01:45"}, "arrival_start": {"2026-10-14T23:00"}, "arrival_end": {"2026-10-14T23:30"}, "assignee_ids": {fmt.Sprint(admin)}}
	body := dispatchPost(t, router, cookie, path, input, 200)
	if client.Job.GetX(ctx, j.ID).StartTime != nil {
		t.Fatal("proposal changed the Job before confirmation")
	}
	match := regexp.MustCompile(`name="review_id" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("missing dispatch review identity: %s", body)
	}
	masterPost(t, router, cookie, path, url.Values{"review_id": {match[1]}}, 303)
	got := client.Job.GetX(ctx, j.ID)
	if got.StartTime == nil || got.EndTime == nil || got.EndTime.Sub(*got.StartTime).Minutes() != 150 || got.ArrivalWindowStart == nil || got.StatusID != nil {
		t.Fatalf("dispatch lost full interval/window or changed status: %+v", got)
	}
	if !client.JobAssignment.Query().Where(jobassignment.JobIDEQ(j.ID), jobassignment.UserIDEQ(admin)).ExistX(ctx) {
		t.Fatal("dispatch omitted assignee")
	}
}

func dispatchPost(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values, want int) string {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("POST %s: %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w.Body.String()
}

func TestJobDispatchDSTHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("DST owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("DST work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/dispatch", j.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, in := range []url.Values{
		{"start": {"2026-03-08T02:30"}, "end": {"2026-03-08T04:00"}},
		{"start": {"2026-11-01T01:30"}, "end": {"2026-11-01T03:00"}},
		{"start": {"2026-11-01T01:30"}, "start_offset": {"-06:00"}, "end": {"2026-11-01T03:00"}},
	} {
		dispatchPost(t, router, cookie, path, in, 422)
	}
	for _, offset := range []string{"-04:00", "-05:00"} {
		body := dispatchPost(t, router, cookie, path, url.Values{"start": {"2026-11-01T01:30"}, "start_offset": {offset}, "end": {"2026-11-01T03:00"}}, 200)
		token := regexp.MustCompile(`name="review_id" value="([^"]+)"`).FindStringSubmatch(body)[1]
		dispatchPost(t, router, cookie, path, url.Values{"review_id": {token}}, 303)
		want, _ := time.Parse(time.RFC3339, "2026-11-01T01:30:00"+offset)
		if got := client.Job.GetX(ctx, j.ID); got.StartTime == nil || !got.StartTime.Equal(want) {
			t.Fatalf("selected offset %s shifted: %v", offset, got.StartTime)
		}
	}
}

func TestJobDispatchOverlapCurrentAcknowledgmentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Overlap owner").SaveX(ctx)
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	other := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Existing overlap").SetStartTime(start).SetEndTime(start.Add(time.Hour)).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(other.ID).SetUserID(admin).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Proposed overlap").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/dispatch", j.ID)
	in := url.Values{"start": {"2026-10-20T09:30"}, "end": {"2026-10-20T10:30"}, "assignee_ids": {fmt.Sprint(admin)}}
	body := dispatchPost(t, router, cookie, path, in, 200)
	assertContains(t, body, "Existing overlap")
	token := regexp.MustCompile(`name="review_id" value="([^"]+)"`).FindStringSubmatch(body)[1]
	dispatchPost(t, router, cookie, path, url.Values{"review_id": {token}}, 409)
	if client.Job.GetX(ctx, j.ID).StartTime != nil {
		t.Fatal("unacknowledged overlap committed")
	}
	// A reviewed acknowledgment cannot cover a newly intersecting Job.
	third := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Racing overlap").SetStartTime(start).SetEndTime(start.Add(2 * time.Hour)).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(third.ID).SetUserID(admin).SaveX(ctx)
	dispatchPost(t, router, cookie, path, url.Values{"review_id": {token}, "acknowledge_overlaps": {"true"}}, 409)
	body = dispatchPost(t, router, cookie, path, in, 200)
	token = regexp.MustCompile(`name="review_id" value="([^"]+)"`).FindStringSubmatch(body)[1]
	dispatchPost(t, router, cookie, path, url.Values{"review_id": {token}, "acknowledge_overlaps": {"true"}}, 303)
	var audited bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE target_type='job' AND target_id=$1 AND detail->>'overlaps_acknowledged'='true')`, j.ID).Scan(&audited); err != nil || !audited {
		t.Fatalf("acknowledgment not audited: %v", err)
	}
	// The identical interval with no common assignee needs no acknowledgment.
	in.Del("assignee_ids")
	body = dispatchPost(t, router, cookie, path, in, 200)
	token = regexp.MustCompile(`name="review_id" value="([^"]+)"`).FindStringSubmatch(body)[1]
	dispatchPost(t, router, cookie, path, url.Values{"review_id": {token}}, 303)
}

func dispatchToken(t *testing.T, body string) string {
	t.Helper()
	m := regexp.MustCompile(`name="review_id" value="([^"]+)"`).FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatalf("missing review token: %s", body)
	}
	return m[1]
}

func TestJobDispatchIndependentPermissionsAndAvailabilityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Independent work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/dispatch", j.ID)
	newUser := func(name, permission string) *http.Cookie {
		t.Helper()
		role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, name, []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: permission, Scope: authorization.All}, {Permission: "directory.view", Scope: authorization.All}})
		if err != nil {
			t.Fatal(err)
		}
		u := client.User.Create().SetCompanyID(company).SetName(name).SetEmail(name + "@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
		if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
			t.Fatal(err)
		}
		return sessionCookie(t, ctx, sessions, u.ID)
	}
	scheduler := newUser("Scheduler-only", "job.schedule")
	assigner := newUser("Assigner-only", "job.assign")
	in := url.Values{"start": {"2026-10-20T09:00"}, "end": {"2026-10-20T10:00"}}
	token := dispatchToken(t, dispatchPost(t, router, scheduler, path, in, 200))
	dispatchPost(t, router, scheduler, path, url.Values{"review_id": {token}}, 303)
	in.Set("assignee_ids", fmt.Sprint(admin))
	dispatchPost(t, router, scheduler, path, in, 403)
	in.Set("start", "2026-10-20T08:00")
	dispatchPost(t, router, assigner, path, in, 403)
	in.Set("start", "2026-10-20T09:00")
	token = dispatchToken(t, dispatchPost(t, router, assigner, path, in, 200))
	dispatchPost(t, router, assigner, path, url.Values{"review_id": {token}}, 303)
	// Assignment-only can unassign without schedule authority.
	in.Del("assignee_ids")
	token = dispatchToken(t, dispatchPost(t, router, assigner, path, in, 200))
	dispatchPost(t, router, assigner, path, url.Values{"review_id": {token}}, 303)
	// Scheduling-only can unschedule without assignment authority.
	token = dispatchToken(t, dispatchPost(t, router, scheduler, path, url.Values{}, 200))
	dispatchPost(t, router, scheduler, path, url.Values{"review_id": {token}}, 303)
	empty, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "No grants", nil)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Unavailable worker").SetEmail("disabled36@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, empty); err != nil {
		t.Fatal(err)
	}
	in = url.Values{"assignee_ids": {fmt.Sprint(u.ID)}}
	body := dispatchPost(t, router, assigner, path, in, 200)
	assertContains(t, body, "incompatible Job View grants")
	token = dispatchToken(t, body)
	dispatchPost(t, router, assigner, path, url.Values{"review_id": {token}}, 303)
	client.User.UpdateOneID(u.ID).SetIsActive(false).SaveX(ctx)
	body = dispatchPost(t, router, assigner, path, in, 200)
	assertContains(t, body, "unavailable")
	token = dispatchToken(t, body)
	dispatchPost(t, router, assigner, path, url.Values{"review_id": {token}}, 303)
	token = dispatchToken(t, dispatchPost(t, router, assigner, path, url.Values{}, 200))
	dispatchPost(t, router, assigner, path, url.Values{"review_id": {token}}, 303)
	dispatchPost(t, router, assigner, path, in, 422)
}

func TestJobDispatchAuditFailureAndStaleDragHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Failure owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Failure work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/dispatch", j.ID)
	for _, legacy := range []string{"/schedule/dispatch", "/schedule/move", "/schedule/calendar/move"} {
		dispatchPost(t, router, cookie, legacy, url.Values{"job_id": {fmt.Sprint(j.ID)}, "date": {"2026-10-20"}, "user_id": {fmt.Sprint(admin)}}, 303)
	}
	if client.Job.GetX(ctx, j.ID).StartTime != nil {
		t.Fatal("legacy drag mutated before review")
	}
	token := dispatchToken(t, dispatchPost(t, router, cookie, path, url.Values{"start": {"2026-10-20T09:00"}, "end": {"2026-10-20T10:00"}, "assignee_ids": {fmt.Sprint(admin)}}, 200))
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_dispatch_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='job' AND NEW.action='job.assign' THEN RAISE EXCEPTION 'injected second-facet audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_dispatch_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_dispatch_audit()`); err != nil {
		t.Fatal(err)
	}
	dispatchPost(t, router, cookie, path, url.Values{"review_id": {token}}, 500)
	if got := client.Job.GetX(ctx, j.ID); got.StartTime != nil || client.JobAssignment.Query().Where(jobassignment.JobIDEQ(j.ID)).ExistX(ctx) {
		t.Fatal("audit failure published partial dispatch")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1`, j.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("second-facet failure left first-facet audit: %d %v", count, err)
	}
}

func TestJobDispatchConcurrentOverlapAndReplayHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race owner").SaveX(ctx)
	paths := []string{}
	tokens := []string{}
	for _, title := range []string{"First concurrent Job", "Second concurrent Job"} {
		j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType(title).SaveX(ctx)
		path := fmt.Sprintf("/jobs/%d/dispatch", j.ID)
		paths = append(paths, path)
		tokens = append(tokens, dispatchToken(t, dispatchPost(t, router, cookie, path, url.Values{"start": {"2026-10-20T09:00"}, "end": {"2026-10-20T10:00"}, "assignee_ids": {fmt.Sprint(admin)}}, 200)))
	}
	start := make(chan struct{})
	results := make(chan [2]int, 2)
	for i := range paths {
		go func(i int) {
			<-start
			r := httptest.NewRequest("POST", paths[i], strings.NewReader(url.Values{"review_id": {tokens[i]}, "acknowledge_overlaps": {"true"}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			results <- [2]int{i, w.Code}
		}(i)
	}
	close(start)
	winner := -1
	conflicts := 0
	for range paths {
		r := <-results
		if r[1] == 303 {
			winner = r[0]
		} else if r[1] == 409 {
			conflicts++
		} else {
			t.Fatalf("concurrent dispatch=%v", r)
		}
	}
	if winner < 0 || conflicts != 1 {
		t.Fatal("both stale no-conflict reviews were accepted")
	}
	var before, after int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action IN ('job.assign','job.schedule')`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE job_dispatch_reviews SET created_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, tokens[winner]); err != nil {
		t.Fatal(err)
	}
	dispatchPost(t, router, cookie, paths[winner], url.Values{"review_id": {tokens[winner]}}, 303)
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action IN ('job.assign','job.schedule')`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != 2 || after != before {
		t.Fatalf("lost-response retry duplicated audit: %d/%d", before, after)
	}
}

func TestJobDispatchCommitTimeAuthorityHTTP(t *testing.T) {
	for _, race := range []string{"session", "grant", "archive", "disabled", "assignment", "overlap"} {
		t.Run(race, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			auth := authorization.New(pool)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Revocable planner", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.schedule", Scope: authorization.All}, {Permission: "job.assign", Scope: authorization.All}, {Permission: "directory.view", Scope: authorization.All}})
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetName("Planner").SetEmail("race36@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race owner").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Race work").SaveX(ctx)
			other := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Racing overlap").SaveX(ctx)
			path := fmt.Sprintf("/jobs/%d/dispatch", j.ID)
			token := dispatchToken(t, dispatchPost(t, router, cookie, path, url.Values{"start": {"2026-10-20T09:00"}, "end": {"2026-10-20T10:00"}, "assignee_ids": {fmt.Sprint(admin)}}, 200))
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
				r := httptest.NewRequest("POST", path, strings.NewReader(url.Values{"review_id": {token}, "acknowledge_overlaps": {"true"}}.Encode()))
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
					t.Fatal("dispatch never reached authority fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch race {
			case "session":
				_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, u.ID)
			case "grant":
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.schedule'`, role)
			case "archive":
				_, err = tx.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, j.ID)
			case "disabled":
				_, err = tx.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, u.ID)
			case "assignment":
				_, err = tx.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, admin)
			case "overlap":
				_, err = tx.Exec(ctx, `UPDATE jobs SET start_time='2026-10-20T09:00:00Z',end_time='2026-10-20T10:00:00Z' WHERE id=$1`, other.ID)
				if err == nil {
					_, err = tx.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, other.ID, admin)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			code := <-done
			if code != 403 && code != 404 && code != 409 {
				t.Fatalf("authority/domain loss accepted: %d", code)
			}
			if client.Job.GetX(ctx, j.ID).StartTime != nil {
				t.Fatal("rejected dispatch partially wrote interval")
			}
		})
	}
}

func TestJobDispatchDueDateIndependentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/Los_Angeles' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Due owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Due work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	masterPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Due work"}, "due_date": {"2020-11-01"}}, 303)
	body := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	assertContains(t, body, `name="due_date" value="2020-11-01"`)
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Overdue")
	var completed int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' ORDER BY id LIMIT 1`, company).Scan(&completed); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, path+"/status", url.Values{"status_id": {fmt.Sprint(completed)}}, 303)
	assertNotContains(t, requestBody(t, router, cookie, "GET", path, 200), "Overdue")
	body = dispatchPost(t, router, cookie, path+"/dispatch", url.Values{"start": {"2026-10-20T09:00"}, "end": {"2026-10-20T10:00"}}, 200)
	assertContains(t, body, "beyond the independent due date")
	dispatchPost(t, router, cookie, path+"/dispatch", url.Values{"review_id": {dispatchToken(t, body)}}, 303)
	if got := client.Job.GetX(ctx, j.ID); got.DueDate.Format("2006-01-02") != "2020-11-01" || got.StatusID == nil || *got.StatusID != completed {
		t.Fatal("dispatch changed due date or Status")
	}
	masterPost(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Due work"}, "due_date": {""}}, 303)
	if client.Job.GetX(ctx, j.ID).DueDate != nil {
		t.Fatal("independent due date could not be cleared")
	}
}

func TestJobDispatchPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser dispatch owner").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Browser equal worker").SetEmail("browser36@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 20, 9, 15, 0, 0, time.UTC)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser dispatched Job").SetStartTime(start).SetEndTime(start.Add(150 * time.Minute)).SetArrivalWindowStart(start.Add(-15 * time.Minute)).SetArrivalWindowEnd(start.Add(15 * time.Minute)).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	overlap := time.Date(2026, 10, 21, 23, 20, 0, 0, time.UTC)
	o := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser conflicting Job").SetStartTime(overlap).SetEndTime(overlap.Add(30 * time.Minute)).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(o.ID).SetUserID(admin).SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	web := middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router))))
	var dropped atomic.Int32
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.Header.Get("X-Test-Lose-Response") == "true" && dropped.CompareAndSwap(0, 1) {
			buffer := httptest.NewRecorder()
			web.ServeHTTP(buffer, r)
			if buffer.Code != 303 {
				t.Errorf("lost response must follow accepted dispatch: %d %s", buffer.Code, buffer.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		web.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/job-dispatch.cjs", server.URL, cookie.Value, fmt.Sprint(j.ID), fmt.Sprint(u.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	if dropped.Load() != 1 {
		t.Fatalf("expected exactly one post-commit response loss, got %d", dropped.Load())
	}
	got := client.Job.GetX(ctx, j.ID)
	want := time.Date(2026, 10, 21, 23, 15, 0, 0, time.UTC)
	if got.StartTime == nil || !got.StartTime.Equal(want) || got.EndTime.Sub(*got.StartTime) != 150*time.Minute || got.StatusID != nil {
		t.Fatal("browser did not persist exact reviewed interval independently of Status")
	}
	if n := client.JobAssignment.Query().Where(jobassignment.JobIDEQ(j.ID)).CountX(ctx); n != 2 {
		t.Fatalf("equal assignees=%d", n)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1 AND action IN ('job.schedule','job.assign')`, j.ID).Scan(&count); err != nil || count != 3 {
		t.Fatalf("browser duplicate or missing effects: %d, %v", count, err)
	}
}

func TestJobDispatchDoesNotExpandPublicFieldAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("API planning forbidden").SetDueDate(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)).SetAssignments(fmt.Sprintf(`[{"user_id":%d,"name":"Recorded worker","role":"legacy-primary"}]`, admin)).SaveX(ctx)
	worker := client.User.Create().SetCompanyID(company).SetName("Unavailable API assignee").SetEmail("unavailable-api36@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(worker.ID).SetRole("legacy-primary").SaveX(ctx)
	client.User.UpdateOneID(worker.ID).SetIsActive(false).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Administrator field device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	get := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d", j.ID), nil)
	get.Header.Set("Authorization", "Bearer "+token)
	shown := httptest.NewRecorder()
	api.ServeHTTP(shown, get)
	if shown.Code != 200 {
		t.Fatalf("API Job read: %d %s", shown.Code, shown.Body.String())
	}
	assertContains(t, shown.Body.String(), `"due_date":"2026-10-20"`)
	assertContains(t, shown.Body.String(), `"name":"Unavailable API assignee","unavailable":true`)
	assertNotContains(t, shown.Body.String(), "legacy-primary")
	for _, path := range []string{fmt.Sprintf("/jobs/%d/dispatch", j.ID), "/schedule/dispatch"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"start":"2026-10-20T09:00:00Z","end":"2026-10-20T10:00:00Z"}`))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("field planning surface %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if client.Job.GetX(ctx, j.ID).StartTime != nil {
		t.Fatal("API planning changed Job")
	}
}

func TestJobDispatchInvalidWholeProposalAndNoopHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Validation owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Validation work").SaveX(ctx)
	j = client.Job.GetX(ctx, j.ID)
	path := fmt.Sprintf("/jobs/%d/dispatch", j.ID)
	for _, in := range []url.Values{
		{"start": {"2026-10-20T09:00"}},
		{"start": {"2026-10-20T09:00"}, "end": {"2026-10-20T09:00"}},
		{"start": {"2026-10-20T10:00"}, "end": {"2026-10-20T09:00"}},
		{"arrival_start": {"2026-10-20T09:00"}, "arrival_end": {"2026-10-20T10:00"}},
		{"start": {"2026-10-20T09:00"}, "end": {"2026-10-20T10:00"}, "arrival_start": {"2026-10-20T09:30"}, "arrival_end": {"2026-10-20T10:00"}},
		{"start": {"2026-10-20T09:00:00.0000001"}, "end": {"2026-10-20T10:00"}},
		{"assignee_ids": {fmt.Sprint(admin), fmt.Sprint(admin)}},
	} {
		dispatchPost(t, router, cookie, path, in, 422)
	}
	if got := client.Job.GetX(ctx, j.ID); got.StartTime != nil || !got.UpdatedAt.Equal(j.UpdatedAt) {
		t.Fatal("invalid proposal changed persisted state")
	}
	token := dispatchToken(t, dispatchPost(t, router, cookie, path, url.Values{}, 200))
	dispatchPost(t, router, cookie, path, url.Values{"review_id": {token}}, 303)
	if got := client.Job.GetX(ctx, j.ID); !got.UpdatedAt.Equal(j.UpdatedAt) {
		t.Fatal("unchanged desired state changed attribution")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1`, j.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("no-op fabricated history: %d %v", count, err)
	}
}

func TestJobDispatchDetailsCannotBypassReviewHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("No bypass owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("No bypass work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	for key, value := range map[string]string{"start_time": "2026-10-20T09:00", "assignments": fmt.Sprintf(`[{"user_id":%d}]`, admin), "visits": `[{"date":"2026-10-20","start_time":"09:00","end_time":"10:00"}]`} {
		in := url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Must roll back"}, key: {value}}
		masterPost(t, router, cookie, path, in, 409)
	}
	got := client.Job.GetX(ctx, j.ID)
	if got.StartTime != nil || got.JobType != j.JobType || got.Visits != "[]" || client.JobAssignment.Query().Where(jobassignment.JobIDEQ(j.ID)).ExistX(ctx) {
		t.Fatal("details mutation bypassed dispatch review")
	}
}

func TestJobDispatchExplicitReviewRefreshesTransportRevisionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Review revision owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Review revision work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/dispatch", j.ID)
	opened := requestBody(t, router, cookie, "GET", path, 200)
	old := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(opened)[1]
	client.Job.UpdateOneID(j.ID).SetNotes("Concurrent notes must survive dispatch").SaveX(ctx)
	body := dispatchPost(t, router, cookie, path, url.Values{"start": {"2026-10-20T09:00"}, "end": {"2026-10-20T10:00"}, "save_action_id": {newOperationKey()}, "save_revision": {old}}, 200)
	current := regexp.MustCompile(`name="save_conflict_review" value="([^"]+)"`).FindStringSubmatch(body)
	if len(current) != 2 || current[1] == old {
		t.Fatal("explicit dispatch review did not capture the current guarded revision")
	}
	dispatchPost(t, router, cookie, path, url.Values{"review_id": {dispatchToken(t, body)}, "save_action_id": {newOperationKey()}, "save_revision": {old}, "save_conflict_review": {current[1]}}, 303)
	got := client.Job.GetX(ctx, j.ID)
	if got.StartTime == nil || got.Notes != "Concurrent notes must survive dispatch" {
		t.Fatal("fresh dispatch review lost unrelated current details")
	}
}
