package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func timeHTTP(router http.Handler, cookie *http.Cookie, path string, v url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func timeFormValues(t *testing.T, router http.Handler, cookie *http.Cookie, path string, v url.Values) url.Values {
	t.Helper()
	copy := url.Values{}
	for key, values := range v {
		copy[key] = append([]string(nil), values...)
	}
	if !timeEntrySavePath(path) || copy.Get("save_action_id") != "" {
		return copy
	}
	formPath := saveFormPath(path)
	r := httptest.NewRequest("GET", formPath, nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code == 200 && strings.Contains(w.Body.String(), `name="save_action_id"`) {
		for _, key := range []string{"save_action_id", "save_revision", "save_history"} {
			if strings.Contains(w.Body.String(), `name="`+key+`"`) {
				copy.Set(key, compositionField(t, w.Body.String(), key))
			}
		}
	}
	return copy
}

func timePost(t *testing.T, router http.Handler, cookie *http.Cookie, path string, v url.Values, want int) {
	t.Helper()
	itemPost(t, router, cookie, path, timeFormValues(t, router, cookie, path, v), want)
}

func TestTimeIndependentScopedActionsHTTP(t *testing.T) {
	for _, scope := range []authorization.Scope{authorization.Own, authorization.Assigned, authorization.All} {
		for _, action := range []string{"create", "correct", "void"} {
			t.Run(string(scope)+"/"+action, func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				auth := authorization.New(pool)
				worker := client.User.Create().SetCompanyID(company).SetEmail("independent-time@test").SetName("Subject").SetRole("admin").SetPasswordHash("hash").SaveX(ctx)
				grants := []authorization.Grant{{Permission: "time.view", Scope: scope}, {Permission: "time." + action, Scope: scope}, {Permission: "job.view", Scope: authorization.All}}
				role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Unrelated role label", grants)
				if err != nil {
					t.Fatal(err)
				}
				if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, worker.ID, role); err != nil {
					t.Fatal(err)
				}
				subject := admin
				if scope == authorization.Own {
					subject = worker.ID
				}
				start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
				create := client.TimeEntry.Create().SetCompanyID(company).SetUserID(subject).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("scoped original")
				var jobID int64
				if scope == authorization.Assigned {
					c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Scoped customer").SaveX(ctx)
					j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Scoped job").SaveX(ctx)
					jobID = j.ID
					client.JobAssignment.Create().SetJobID(j.ID).SetUserID(worker.ID).SaveX(ctx)
					create.SetJobID(j.ID)
				}
				entry := create.SaveX(ctx)
				cookie := sessionCookie(t, ctx, sessions, worker.ID)
				path := fmt.Sprintf("/time-entries/%d", entry.ID)
				manual := url.Values{"user_id": {fmt.Sprint(subject)}, "clock_in": {"2026-01-02T10:00:00Z"}, "clock_out": {"2026-01-02T11:00:00Z"}}
				if jobID > 0 {
					manual.Set("job_id", fmt.Sprint(jobID))
				}
				for _, command := range []struct {
					action, path string
					values       url.Values
				}{{"create", "/time-entries", manual}, {"correct", path, url.Values{"notes": {"scoped corrected"}, "reason": {"scope review"}}}, {"void", path + "/void", url.Values{"reason": {"scope void review"}}}} {
					want := 403
					if command.action == action {
						want = 303
					}
					timePost(t, router, cookie, command.path, command.values, want)
				}
				if scope == authorization.Own {
					other := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SaveX(ctx)
					expectStatus(t, router, cookie, "GET", fmt.Sprintf("/time-entries/%d", other.ID), 404)
				}
				if scope == authorization.Assigned {
					client.JobAssignment.Delete().ExecX(ctx)
					expectStatus(t, router, cookie, "GET", path, 404)
					manual.Set("clock_in", "2026-01-03T10:00:00Z")
					manual.Set("clock_out", "2026-01-03T11:00:00Z")
					timePost(t, router, cookie, "/time-entries", manual, 403)
				}
			})
		}
	}
}

func TestTimeConcurrentReopenClockAndVoidCorrectionHTTP(t *testing.T) {
	for _, race := range []string{"reopen-clock", "void-correction"} {
		t.Run(race, func(t *testing.T) {
			_, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			start := time.Now().Add(-2 * time.Hour).Truncate(time.Microsecond)
			entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("race original").SaveX(ctx)
			path := fmt.Sprintf("/time-entries/%d", entry.ID)
			form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
			first := url.Values{"reason": {"reviewed race"}, "save_action_id": {compositionField(t, form, "save_action_id")}, "save_history": {compositionField(t, form, "save_history")}, "save_revision": {compositionField(t, form, "save_revision")}}
			second := url.Values{}
			secondPath := "/time-entries/clock-in"
			if race == "reopen-clock" {
				first.Set("clock_out", "")
			} else {
				first.Set("notes", "race correction")
				secondPath = path + "/void"
				second = url.Values{"reason": {"race void"}, "save_action_id": {uuid.NewString()}, "save_history": {first.Get("save_history")}, "save_revision": {first.Get("save_revision")}}
			}
			release := make(chan struct{})
			results := make(chan *httptest.ResponseRecorder, 2)
			go func() { <-release; results <- timeHTTP(router, cookie, path, first) }()
			go func() { <-release; results <- timeHTTP(router, cookie, secondPath, second) }()
			close(release)
			a, b := <-results, <-results
			if !((a.Code == 303 && b.Code == 409) || (a.Code == 409 && b.Code == 303)) {
				t.Fatalf("race outcomes %d/%d: %s / %s", a.Code, b.Code, a.Body, b.Body)
			}
			body := requestBody(t, router, cookie, "GET", path, 200)
			assertContains(t, body, "race original")
			if race == "reopen-clock" {
				var active int
				for _, e := range client.TimeEntry.Query().AllX(ctx) {
					if e.ClockOut == nil && e.VoidedAt == nil {
						active++
					}
				}
				if active != 1 {
					t.Fatalf("active=%d", active)
				}
			}
		})
	}
}

func TestTimeAPIClocksOnlyGPSAndOperationRegistryHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	token, err := sessions.CreateMobile(ctx, admin, "time API contract")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	server := httptest.NewServer(api)
	defer server.Close()
	operation := uuid.NewString()
	command := map[string]any{"operation_id": operation, "gps": map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": time.Now().Add(-time.Second).Truncate(time.Microsecond), "accuracy": 4}}
	status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-in", command)
	if status != 200 {
		t.Fatalf("clock=%d %s", status, body)
	}
	entry := client.TimeEntry.Query().OnlyX(ctx)
	path := fmt.Sprintf("/time-entries/%d", entry.ID)
	var before, after string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g))::text FROM time_clock_gps g`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/time-entries", path, path + "/correct", path + "/void"} {
		for _, method := range []string{"POST", "PATCH", "DELETE"} {
			status, _ := clockReviewCall(t, server, token, method, target, map[string]any{"operation_id": uuid.NewString(), "reason": "broad admin does not expand API"})
			if status != 404 && status != 405 && status != 403 {
				t.Fatalf("field API %s %s=%d", method, target, status)
			}
		}
	}
	form := requestBody(t, router, cookie, "GET", "/time-entries/new", 200)
	manual := url.Values{"user_id": {fmt.Sprint(admin)}, "clock_in": {"2026-01-01T10:00:00Z"}, "clock_out": {"2026-01-01T11:00:00Z"}, "save_action_id": {operation}, "save_history": {compositionField(t, form, "save_history")}}
	itemPost(t, router, cookie, "/time-entries", manual, 409)
	timePost(t, router, cookie, path, url.Values{"clock_in": {entry.ClockIn.Add(-time.Minute).Format(time.RFC3339Nano)}, "reason": {"Correct clock, never move observation"}}, 303)
	timePost(t, router, cookie, path+"/void", url.Values{"reason": {"Terminal GPS retention"}}, 303)
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g))::text FROM time_clock_gps g`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("correction/Void changed GPS")
	}
	status, body = clockReviewCall(t, server, token, "POST", "/time-entries/clock-out", map[string]any{"operation_id": uuid.NewString(), "entry_id": entry.ID})
	if status != 409 || !strings.Contains(body, "time_void") {
		t.Fatalf("void out=%d %s", status, body)
	}
	status, body = clockReviewCall(t, server, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString()})
	if status != 200 {
		t.Fatalf("new API clock after Void=%d %s", status, body)
	}
}

func TestTimeAuthorityLossAtCommitHTTP(t *testing.T) {
	for _, action := range []string{"create", "correct", "void"} {
		t.Run(action, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			auth := authorization.New(pool)
			worker := client.User.Create().SetCompanyID(company).SetEmail("waiting-time@test").SetName("Waiting subject").SetRole("admin").SetPasswordHash("hash").SaveX(ctx)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Revoked while waiting", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time." + action, Scope: authorization.Own}})
			if err != nil {
				t.Fatal(err)
			}
			if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, worker.ID, role); err != nil {
				t.Fatal(err)
			}
			start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
			entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(worker.ID).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("unchanged under revocation").SaveX(ctx)
			path := fmt.Sprintf("/time-entries/%d", entry.ID)
			v := url.Values{"notes": {"must not commit"}, "reason": {"authority race"}}
			if action == "void" {
				path += "/void"
			}
			if action == "create" {
				path = "/time-entries"
				v = url.Values{"user_id": {fmt.Sprint(worker.ID)}, "clock_in": {"2026-01-02T10:00:00Z"}, "clock_out": {"2026-01-02T11:00:00Z"}}
			}
			cookie := sessionCookie(t, ctx, sessions, worker.ID)
			v = timeFormValues(t, router, cookie, path, v)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- timeHTTP(router, cookie, path, v) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=190019 AND NOT granted)`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("command did not reach authority fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, "time."+action); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			response := <-done
			if response.Code != 403 {
				t.Fatalf("revoked=%d %s", response.Code, response.Body)
			}
			if client.TimeEntry.Query().CountX(ctx) != 1 || client.TimeEntry.GetX(ctx, entry.ID).VoidedAt != nil || client.TimeEntry.GetX(ctx, entry.ID).Notes != "unchanged under revocation" {
				t.Fatal("revoked command committed")
			}
		})
	}
}

func TestTimeAuditFailureAllActionsHTTP(t *testing.T) {
	for _, action := range []string{"create", "correct", "void"} {
		t.Run(action, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
			entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("before failed audit").SaveX(ctx)
			path := fmt.Sprintf("/time-entries/%d", entry.ID)
			formPath := path + "/edit"
			values := url.Values{"reason": {"audit recovery"}, "notes": {"after audit recovery"}}
			if action == "void" {
				path += "/void"
			}
			if action == "create" {
				path = "/time-entries"
				formPath = path + "/new"
				values = url.Values{"user_id": {fmt.Sprint(admin)}, "clock_in": {"2026-01-02T10:00:00Z"}, "clock_out": {"2026-01-02T11:00:00Z"}}
			}
			form := requestBody(t, router, cookie, "GET", formPath, 200)
			op := uuid.NewString()
			values.Set("save_action_id", op)
			values.Set("save_history", compositionField(t, form, "save_history"))
			if action != "create" {
				values.Set("save_revision", compositionField(t, form, "save_revision"))
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_time_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'time.%' THEN RAISE EXCEPTION 'time audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_time_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_time_audit()`); err != nil {
				t.Fatal(err)
			}
			itemPost(t, router, cookie, path, values, 503)
			var effects int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_admissions WHERE operation_id=$1)+(SELECT count(*) FROM time_entry_history)`, op).Scan(&effects); err != nil || effects != 1 {
				t.Fatalf("failure must retain only UUID binding: count=%d err=%v", effects, err)
			}
			if client.TimeEntry.Query().CountX(ctx) != 1 || client.TimeEntry.GetX(ctx, entry.ID).VoidedAt != nil || client.TimeEntry.GetX(ctx, entry.ID).Notes != "before failed audit" {
				t.Fatal("audit failure left partial effects")
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER reject_time_audit ON authorization_audit`); err != nil {
				t.Fatal(err)
			}
			itemPost(t, router, cookie, path, values, 303)
		})
	}
}

func TestTimeBrowserRequiresOperationIdentityHTTP(t *testing.T) {
	_, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	v := url.Values{"user_id": {fmt.Sprint(admin)}, "clock_in": {"2026-01-01T10:00:00Z"}, "clock_out": {"2026-01-01T11:00:00Z"}}
	itemPost(t, router, cookie, "/time-entries", v, 428)
	if client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatal("unkeyed browser mutation committed")
	}
}

func TestTimeRejectedBrowserIdentityCannotBecomeClockHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	form := requestBody(t, router, cookie, "GET", "/time-entries/new", 200)
	operation := uuid.NewString()
	v := url.Values{"user_id": {fmt.Sprint(admin)}, "clock_in": {"2026-01-01T11:00:00Z"}, "clock_out": {"2026-01-01T10:00:00Z"}, "save_action_id": {operation}, "save_history": {compositionField(t, form, "save_history")}}
	itemPost(t, router, cookie, "/time-entries", v, 409)
	token, err := sessions.CreateMobile(ctx, admin, "collision after rejection")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": operation})
	if status != 409 {
		t.Fatalf("rejected manual UUID rebound to clock: %d %s", status, body)
	}
	if client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatal("cross-family rejected identity acquired an effect")
	}
}
