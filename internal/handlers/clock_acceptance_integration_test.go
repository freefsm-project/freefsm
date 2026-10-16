package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"
)

// The out target is the entry the user actually reviewed, never whichever
// entry happens to be active when a delayed request reaches the server.
func TestClockExactEntryHTTP(t *testing.T) {
	_, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	post := func(path string, values url.Values, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s = %d want %d: %s", path, w.Code, want, w.Body.String())
		}
	}
	post("/time-entries/clock-in", nil, 303)
	first := client.TimeEntry.Query().OnlyX(ctx)
	post("/time-entries/clock-out", url.Values{"entry_id": {fmt.Sprint(first.ID)}}, 303)
	post("/time-entries/clock-in", nil, 303)
	post("/time-entries/clock-out", url.Values{"entry_id": {fmt.Sprint(first.ID)}}, 409)
	entries := client.TimeEntry.Query().AllX(ctx)
	active := 0
	for _, e := range entries {
		if e.ClockOut == nil {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("stale out closed the newer entry: active=%d", active)
	}
}

func TestClockExactElapsedHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 23, 59, 30, 250000000, time.UTC)
	end := time.Date(2026, 1, 2, 0, 0, 30, 750000000, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(end).SaveX(ctx)
	body := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/time-entries/%d", entry.ID), 200)
	assertContains(t, body, "1m0.5s")
}

func TestClockOwnAuthorityCloseoutAndAuditHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	user := client.User.Create().SetCompanyID(company).SetEmail("clock-worker@test").SetName("Clock worker").SetRole("dispatcher").SetPasswordHash("hash").SaveX(ctx)
	grants := []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.clock_in", Scope: authorization.Own}, {Permission: "time.clock_out", Scope: authorization.Own}, {Permission: "time.gps.capture", Scope: authorization.Own}, {Permission: "job.view", Scope: authorization.Assigned}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Clock only", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Hidden customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Hidden job").SaveX(ctx)
	completed, err := statusflow.New(pool).Default(ctx, company, statusflow.Job, statusflow.JobCompleted)
	if err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).TransitionJob(authorization.WithActor(ctx, svc, authorization.Actor{UserID: admin, Channel: "web"}), statusflow.Actor{ID: admin, CompanyID: company}, job.ID, completed.ID); err != nil {
		t.Fatal(err)
	}
	assignment := client.JobAssignment.Create().SetJobID(job.ID).SetUserID(user.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	post := func(path string, v url.Values, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s=%d want %d: %s", path, w.Code, want, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "Hidden") {
			t.Fatal("clock disclosed inaccessible context")
		}
	}
	path := fmt.Sprintf("/jobs/%d/clock-in", job.ID)
	post(path, url.Values{"user_id": {fmt.Sprint(admin)}}, 303)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	if entry.UserID != user.ID {
		t.Fatal("clock targeted submitted user")
	}
	otherOut := httptest.NewRequest("POST", "/time-entries/clock-out", strings.NewReader(url.Values{"entry_id": {fmt.Sprint(entry.ID)}}.Encode()))
	otherOut.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	otherOut.AddCookie(sessionCookie(t, ctx, sessions, admin))
	otherResponse := httptest.NewRecorder()
	router.ServeHTTP(otherResponse, otherOut)
	if otherResponse.Code != 404 || client.TimeEntry.GetX(ctx, entry.ID).ClockOut != nil {
		t.Fatalf("Administrator clocked another user: %d", otherResponse.Code)
	}
	client.JobAssignment.DeleteOne(assignment).ExecX(ctx)
	client.Job.UpdateOneID(job.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	post(fmt.Sprintf("/jobs/%d/clock-out", job.ID), url.Values{"entry_id": {fmt.Sprint(entry.ID)}, "latitude": {"51.5"}, "longitude": {"-0.12"}, "captured_at": {time.Now().Add(-time.Second).Truncate(time.Microsecond).Format(time.RFC3339Nano)}, "accuracy": {"0"}}, 303)
	if client.TimeEntry.GetX(ctx, entry.ID).JobID == nil {
		t.Fatal("closeout lost Job association")
	}
	show := fmt.Sprintf("/time-entries/%d", entry.ID)
	assertNotContains(t, requestBody(t, router, cookie, "GET", show, 200), "51.5")
	assertContains(t, requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", show, 200), "Clock-out GPS")
	post(path, nil, 404)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_clock_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='time.clock_in' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_clock_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_clock_audit()`); err != nil {
		t.Fatal(err)
	}
	post("/time-entries/clock-in", nil, 409)
	if client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatal("audit failure retained clock effect")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_clock_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Clock only", []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.create", Scope: authorization.All}, {Permission: "time.correct", Scope: authorization.All}}, 1); err != nil {
		t.Fatal(err)
	}
	post("/time-entries/clock-in", nil, 403)
}

func TestClockConcurrentHTTP(t *testing.T) {
	_, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	start := make(chan struct{})
	codes := make(chan int, 6)
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := httptest.NewRequest("POST", "/time-entries/clock-in", nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	close(start)
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 303 {
			success++
		} else if code != 409 {
			t.Fatalf("racing clock-in=%d", code)
		}
	}
	if success != 1 || client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatalf("race accepted %d clocks", success)
	}
}

func TestClockCorrectionOverlapHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	start := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	first := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start.Add(2 * time.Hour)).SetClockOut(start.Add(3 * time.Hour)).SaveX(ctx)
	values := url.Values{"reason": {"Review overlapping end"}, "clock_out": {start.Add(150 * time.Minute).Format("2006-01-02T15:04:05")}}
	cookie := sessionCookie(t, ctx, sessions, admin)
	values = timeFormValues(t, router, cookie, fmt.Sprintf("/time-entries/%d", first.ID), values)
	r := httptest.NewRequest("POST", fmt.Sprintf("/time-entries/%d", first.ID), strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatalf("overlapping correction=%d: %s", w.Code, w.Body.String())
	}
	if !client.TimeEntry.GetX(ctx, first.ID).ClockOut.Equal(start.Add(time.Hour)) {
		t.Fatal("rejected correction changed original interval")
	}
}

func TestClockPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	evidence := os.Getenv("CLOCK_BROWSER_EVIDENCE")
	if evidence == "" {
		evidence = t.TempDir()
	} else if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("node", "testdata/browser/clocks.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, evidence).CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
	var entries, gps int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM time_entries WHERE clock_out IS NOT NULL`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM time_clock_gps`).Scan(&gps); err != nil {
		t.Fatal(err)
	}
	if entries != 1 || gps != 1 {
		t.Fatalf("persisted closed entries=%d GPS=%d want 1,1", entries, gps)
	}
}

func TestClockReferenceConformanceHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "Clock reference harness")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	output, err := exec.Command("node", "../api/v1/testdata/clock-reference-conformance.mjs", server.URL, token).CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
	if client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatal("reference harness duplicated time")
	}
}

func TestClockAuthorityLossWhileWaitingHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	user := client.User.Create().SetCompanyID(company).SetEmail("clock-race@test").SetName("Clock race").SetRole("admin").SetPasswordHash("hash").SaveX(ctx)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Clock race grants", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.clock_in", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", "/time-entries/clock-in", nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		result <- w.Code
	}()
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
			t.Fatal("clock did not wait at authority fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='time.clock_in'`, role); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-result; code != 403 {
		t.Fatalf("revoked waiting clock=%d", code)
	}
	if client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatal("waiting command committed after grant loss")
	}
}

func TestClockManualAndCorrectionShareInvariant(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin, Channel: "web"}
	start := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Microsecond)
	end := start.Add(time.Hour)
	id, err := svc.CreateManualTimeEntry(ctx, actor, authorization.ManualTimeEntry{UserID: admin, ClockIn: &start, ClockOut: &end})
	if err != nil {
		t.Fatal(err)
	}
	overlap := start.Add(time.Minute)
	if _, err := svc.CreateManualTimeEntry(ctx, actor, authorization.ManualTimeEntry{UserID: admin, ClockIn: &overlap, ClockOut: &end}); err == nil {
		t.Fatal("manual writer accepted overlap")
	}
	if _, err := svc.CreateManualTimeEntry(ctx, actor, authorization.ManualTimeEntry{UserID: admin, ClockIn: &start}); err == nil {
		t.Fatal("manual writer accepted active entry")
	}
	entry, err := svc.Clock(ctx, actor, false, authorization.ClockCommand{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.CorrectTimeEntry(ctx, actor, entry.ID, authorization.TimeEntryCorrection{Reason: "Review overlap", ClockIn: &overlap}); err == nil {
		t.Fatal("correction overlapped manual entry")
	}
	if client.TimeEntry.GetX(ctx, id).CompanyID == nil || *client.TimeEntry.GetX(ctx, id).CompanyID != company {
		t.Fatal("manual subject partition lost")
	}
}

func TestClockUpgradePreservesRecordedGPSAndGrants(t *testing.T) {
	old := fstest.MapFS{}
	source := database.MigrationFS()
	files, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		// Install the historical schema immediately before Clock evidence.
		if file.IsDir() || file.Name() >= "076_clock_evidence.up.sql" {
			continue
		}
		data, err := fs.ReadFile(source, file.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[file.Name()] = &fstest.MapFile{Data: data}
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	start := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	// Use the installed schema, not the candidate Ent builder's new columns.
	entry := struct {
		ID    int64
		Notes string
	}{Notes: "Known historical incomplete capture"}
	if err := pool.QueryRow(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in,latitude,notes) VALUES($1,$2,$3,51.234567,$4) RETURNING id`, company, admin, start, entry.Notes).Scan(&entry.ID); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope),'[]')::text FROM authorization_grants g`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := (&database.DB{Pool: pool}).Migrate(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope),'[]')::text FROM authorization_grants g`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("upgrade changed Role grants")
	}
	got := client.TimeEntry.GetX(ctx, entry.ID)
	if got.ClockOut != nil || !got.ClockIn.Equal(start) || got.Latitude == nil || *got.Latitude != 51.234567 || got.Longitude != nil || got.Notes != entry.Notes {
		t.Fatal("upgrade invented or changed recorded history")
	}
	var captures int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM time_clock_gps`).Scan(&captures); err != nil {
		t.Fatal(err)
	}
	if captures != 0 {
		t.Fatal("upgrade invented GPS capture")
	}
	body := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/time-entries/%d", entry.ID), 200)
	assertContains(t, body, "capture time not recorded")
}

func TestClockPendingLegacyReviewDeniesWithoutMutation(t *testing.T) {
	_, client, router, sessions, _, admin := permissionHTTPFixture(t, true)
	ctx := context.Background()
	r := httptest.NewRequest("POST", "/time-entries/clock-in", nil)
	r.AddCookie(sessionCookie(t, ctx, sessions, admin))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "Administrator review") {
		t.Fatalf("pending clock=%d: %s", w.Code, w.Body.String())
	}
	if client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatal("pending cutover accepted clock")
	}
}

func TestClockManualRaceDoesNotOverlap(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	token, err := sessions.CreateMobile(ctx, admin, "Manual clock race")
	if err != nil {
		t.Fatal(err)
	}
	apiActor := authorization.Actor{UserID: admin, Channel: "api", SessionHash: fmt.Sprintf("%x", sha256.Sum256([]byte(token)))}
	lease, err := svc.ClockLease(ctx, apiActor, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	start := lease.IssuedAt.Add(time.Microsecond)
	end := time.Now().Add(-time.Microsecond).UTC().Truncate(time.Microsecond)
	occurrence := start.Add(time.Microsecond)
	release := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-release
		_, err := svc.CreateManualTimeEntry(ctx, authorization.Actor{UserID: admin}, authorization.ManualTimeEntry{UserID: admin, ClockIn: &start, ClockOut: &end})
		results <- err
	}()
	go func() {
		<-release
		_, err := svc.SubmitClock(ctx, apiActor, false, authorization.ClockCommand{OperationID: uuid.NewString(), LeaseID: lease.ID, OccurredAt: &occurrence})
		results <- err
	}()
	close(release)
	accepted, conflicted := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			accepted++
		} else {
			var conflict *authorization.ClockConflict
			if !errors.As(err, &conflict) || conflict.Code != "time_overlap" {
				t.Fatal(err)
			}
			conflicted++
		}
	}
	if accepted != 1 || conflicted != 1 || client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatalf("manual/clock race accepted=%d conflict=%d", accepted, conflicted)
	}
}

func TestClockAssignmentAndArchiveRacesHTTP(t *testing.T) {
	for _, change := range []string{"assignment", "archive"} {
		t.Run(change, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			svc := authorization.New(pool)
			u := client.User.Create().SetCompanyID(company).SetEmail("job-clock-race@test").SetName("Job clock race").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Job clock", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.clock_in", Scope: authorization.Own}, {Permission: "job.view", Scope: authorization.Assigned}})
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Clock race customer").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Clock race job").SaveX(ctx)
			client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				r := httptest.NewRequest("POST", fmt.Sprintf("/jobs/%d/clock-in", j.ID), nil)
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w.Code
			}()
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
					t.Fatal("request never waited")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if change == "assignment" {
				_, err = tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID)
			} else {
				_, err = tx.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, j.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if code := <-done; code != 404 {
				t.Fatalf("lost %s clock=%d", change, code)
			}
			if client.TimeEntry.Query().CountX(ctx) != 0 {
				t.Fatal("lost Job clock fell back or committed")
			}
		})
	}
}

func TestClockGPSGrantIsIndependentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	u := client.User.Create().SetCompanyID(company).SetEmail("no-gps-clock@test").SetName("No GPS clock").SetRole("admin").SetPasswordHash("hash").SaveX(ctx)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Clocks without GPS", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.clock_in", Scope: authorization.Own}, {Permission: "time.clock_out", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	post := func(path string, v url.Values, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s=%d want %d: %s", path, w.Code, want, w.Body.String())
		}
	}
	gps := url.Values{"latitude": {"51.5"}, "longitude": {"-0.12"}, "captured_at": {time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)}}
	post("/time-entries/clock-in", gps, 403)
	if client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatal("denied GPS silently discarded")
	}
	post("/time-entries/clock-in", nil, 303)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	gps.Set("entry_id", fmt.Sprint(entry.ID))
	post("/time-entries/clock-out", gps, 403)
	if client.TimeEntry.GetX(ctx, entry.ID).ClockOut != nil {
		t.Fatal("denied close-out GPS silently discarded")
	}
	post("/time-entries/clock-out", url.Values{"entry_id": {fmt.Sprint(entry.ID)}}, 303)
}

func TestClockReceiptSurvivesJobAndGrantLossHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	u := client.User.Create().SetCompanyID(company).SetEmail("clock-receipt@test").SetName("Clock receipt").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Clock receipts", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.clock_in", Scope: authorization.Own}, {Permission: "time.clock_out", Scope: authorization.Own}, {Permission: "job.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE clock customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("PRIVATE clock job").SaveX(ctx)
	assignment := client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, u.ID, "Receipt device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path string, body any, want int) string {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s=%d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	operation := uuid.NewString()
	command := map[string]any{"operation_id": operation, "notes": "PRIVATE entered notes"}
	path := fmt.Sprintf("/jobs/%d/clock-in", j.ID)
	first := call("POST", path, command, 200)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	client.JobAssignment.DeleteOne(assignment).ExecX(ctx)
	client.Job.UpdateOneID(j.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	receipt := call("GET", "/time-operations/"+operation, nil, 200)
	if receipt != first || strings.Contains(receipt, "PRIVATE") || strings.Contains(receipt, "job_id") || strings.Contains(receipt, "latitude") {
		t.Fatalf("unsafe or changed receipt: %s", receipt)
	}
	if replay := call("POST", path, command, 200); replay != first {
		t.Fatal("scope loss changed replay")
	}
	command["notes"] = "changed"
	call("POST", path, command, 409)
	call("POST", "/time-entries/clock-out", map[string]any{"operation_id": uuid.NewString(), "entry_id": entry.ID}, 200)
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Clock receipts", nil, 1); err != nil {
		t.Fatal(err)
	}
	if receipt := call("GET", "/time-operations/"+operation, nil, 200); receipt != first {
		t.Fatal("action-grant loss hid terminal receipt")
	}
	call("POST", path, map[string]any{"operation_id": uuid.NewString()}, 404)
	if client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatal("lost-response reconciliation duplicated time")
	}
}

func TestClockActivityFailureRollsBackHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	post := func(path string, v url.Values, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s=%d want %d: %s", path, w.Code, want, w.Body.String())
		}
	}
	post("/time-entries/clock-in", nil, 303)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_clock_activity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('clocked_in','clocked_out') THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_clock_activity BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION fail_clock_activity()`); err != nil {
		t.Fatal(err)
	}
	post("/time-entries/clock-out", url.Values{"entry_id": {fmt.Sprint(entry.ID)}}, 409)
	if client.TimeEntry.GetX(ctx, entry.ID).ClockOut != nil {
		t.Fatal("activity failure retained out effect")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_clock_activity ON activity_logs`); err != nil {
		t.Fatal(err)
	}
	post("/time-entries/clock-out", url.Values{"entry_id": {fmt.Sprint(entry.ID)}}, 303)
	if _, err := pool.Exec(ctx, `CREATE TRIGGER fail_clock_activity BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION fail_clock_activity()`); err != nil {
		t.Fatal(err)
	}
	post("/time-entries/clock-in", nil, 409)
	if client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatal("activity failure retained in effect")
	}
}

func TestClockOfflineCrossMidnightHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "Previously staged cross-midnight work")
	if err != nil {
		t.Fatal(err)
	}
	// A controlled prior-issued lease fixture represents already staged work.
	midnight := time.Now().UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
	start := midnight.Add(-29750 * time.Millisecond)
	end := midnight.Add(30750 * time.Millisecond)
	lease := uuid.NewString()
	key := "api:" + fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	if _, err := pool.Exec(ctx, `INSERT INTO time_clock_leases(id,user_id,client_key,issued_at,expires_at) VALUES($1,$2,$3,$4,$5)`, lease, admin, key, start.Add(-time.Hour), start.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	post := func(path string, body any) {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s=%d: %s", path, w.Code, w.Body.String())
		}
	}
	post("/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString(), "lease_id": lease, "occurred_at": start, "gps": map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": start, "accuracy": 0}})
	entry := client.TimeEntry.Query().OnlyX(ctx)
	post("/time-entries/clock-out", map[string]any{"operation_id": uuid.NewString(), "entry_id": entry.ID, "lease_id": lease, "occurred_at": end, "gps": map[string]any{"latitude": 51.6, "longitude": -0.13, "captured_at": end}})
	got := client.TimeEntry.GetX(ctx, entry.ID)
	if !got.ClockIn.Equal(start) || got.ClockOut == nil || !got.ClockOut.Equal(end) {
		t.Fatal("offline occurrence was shifted, rounded or split")
	}
	body := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/time-entries/%d", entry.ID), 200)
	assertContains(t, body, "1m0.5s")
	assertContains(t, body, "Clock-in GPS")
	assertContains(t, body, "Clock-out GPS")
}

func TestClockOfflineReceiptHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "Clock acceptance")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	request := func(path string, body any, want int) []byte {
		t.Helper()
		data, _ := json.Marshal(body)
		r := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		if path == "/time-entries/lease" {
			r.Header.Set("Operation-ID", uuid.NewString())
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s=%d want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.Bytes()
	}
	var response struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(request("/time-entries/lease", map[string]any{}, 200), &response); err != nil {
		t.Fatal(err)
	}
	lease := response.Data
	in := map[string]any{"operation_id": uuid.NewString(), "occurred_at": time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), "lease_id": lease.ID}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_clock_api_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='time.clock_in' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_clock_api_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_clock_api_audit()`); err != nil {
		t.Fatal(err)
	}
	request("/time-entries/clock-in", in, 503)
	lookup := httptest.NewRequest("GET", "/time-operations/"+in["operation_id"].(string), nil)
	lookup.Header.Set("Authorization", "Bearer "+token)
	unknown := httptest.NewRecorder()
	api.ServeHTTP(unknown, lookup)
	if unknown.Code != 404 {
		t.Fatalf("failed effect has terminal receipt: %s", unknown.Body.String())
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_clock_api_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	first := request("/time-entries/clock-in", in, 200)
	if got := request("/time-entries/clock-in", in, 200); string(got) != string(first) {
		t.Fatalf("receipt changed: %s -> %s", first, got)
	}
	if client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatal("replayed clock created twice")
	}
	entry := client.TimeEntry.Query().OnlyX(ctx)
	out := map[string]any{"operation_id": uuid.NewString(), "entry_id": entry.ID, "occurred_at": time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), "lease_id": lease.ID}
	request("/time-entries/clock-out", out, 200)
	out["operation_id"] = uuid.NewString()
	request("/time-entries/clock-out", out, 200)
	out["operation_id"] = uuid.NewString()
	out["occurred_at"] = time.Now().UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	request("/time-entries/clock-out", out, 409)
	in["operation_id"] = uuid.NewString()
	in["occurred_at"] = time.Now().UTC().Truncate(time.Microsecond).Add(time.Nanosecond).Format(time.RFC3339Nano)
	request("/time-entries/clock-in", in, 400)
	if client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatal("unsupported precision was silently rounded")
	}
}

func TestClockRejectsOverlapAndInvalidGPSHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	post := func(v url.Values, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/time-entries/clock-in", strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("clock-in=%d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	post(url.Values{"latitude": {"51"}}, 400)
	raw := httptest.NewRequest("POST", "/time-entries/clock-in", strings.NewReader(`{"gps":{"latitude":51}}`))
	raw.Header.Set("Content-Type", "application/json")
	raw.AddCookie(cookie)
	wrongType := httptest.NewRecorder()
	router.ServeHTTP(wrongType, raw)
	if wrongType.Code != 415 {
		t.Fatalf("unsupported web body silently discarded: %d", wrongType.Code)
	}
	if client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatal("invalid GPS was silently discarded")
	}
	now := time.Now()
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(now.Add(-time.Hour)).SetClockOut(now.Add(time.Hour)).SaveX(ctx)
	post(nil, 409)
	if client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatal("clock overlaps retained historical entry")
	}
}
