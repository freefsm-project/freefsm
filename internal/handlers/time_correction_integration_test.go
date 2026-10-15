package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/timeentry"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestTimeCorrectionRequiresReasonHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("original fact").SaveX(ctx)
	r := httptest.NewRequest("POST", fmt.Sprintf("/time-entries/%d", entry.ID), strings.NewReader(url.Values{"notes": {"changed without reason"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(sessionCookie(t, ctx, sessions, admin))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("reasonless correction = %d: %s", w.Code, w.Body.String())
	}
	body := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/time-entries/%d", entry.ID), 200)
	assertContains(t, body, "original fact")
	assertNotContains(t, body, "changed without reason")
}

func TestTimeOwnRetainsSafeJobSnapshotHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	worker := client.User.Create().SetCompanyID(company).SetEmail("time-own@test").SetName("Time subject").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Time Own", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.create", Scope: authorization.Own}, {Permission: "time.correct", Scope: authorization.Own}, {Permission: "job.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, worker.ID, role); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE CUSTOMER").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Retained original title").SetNotes("PRIVATE JOB DETAIL").SaveX(ctx)
	assignment := client.JobAssignment.Create().SetJobID(job.ID).SetUserID(worker.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, worker.ID)
	timePost(t, router, cookie, "/time-entries", url.Values{"user_id": {fmt.Sprint(worker.ID)}, "job_id": {fmt.Sprint(job.ID)}, "clock_in": {"2026-01-01T10:00:00Z"}, "clock_out": {"2026-01-01T11:00:00Z"}}, 303)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	path := fmt.Sprintf("/time-entries/%d", entry.ID)
	client.JobAssignment.DeleteOne(assignment).ExecX(ctx)
	client.Job.UpdateOneID(job.ID).SetJobType("PRIVATE NEW TITLE").SaveX(ctx)
	for _, suffix := range []string{"", "/edit"} {
		body := requestBody(t, router, cookie, "GET", path+suffix, 200)
		assertContains(t, body, "Retained original title")
		for _, secret := range []string{"PRIVATE CUSTOMER", "PRIVATE JOB DETAIL", "PRIVATE NEW TITLE", fmt.Sprintf(`href="/jobs/%d"`, job.ID)} {
			assertNotContains(t, body, secret)
		}
	}
	timePost(t, router, cookie, path, url.Values{"job_id": {fmt.Sprint(job.ID)}, "reason": {"Keep retained link"}}, 303)
	timePost(t, router, cookie, path, url.Values{"job_id": {""}, "reason": {"Clear retained link"}}, 303)
	if client.TimeEntry.GetX(ctx, entry.ID).JobID != nil {
		t.Fatal("own subject cannot clear lost-access link")
	}
	eligible := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Currently authorized replacement").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(eligible.ID).SetUserID(worker.ID).SaveX(ctx)
	timePost(t, router, cookie, path, url.Values{"job_id": {fmt.Sprint(eligible.ID)}, "reason": {"Choose current authorized Job"}}, 303)
	if got := client.TimeEntry.GetX(ctx, entry.ID).JobID; got == nil || *got != eligible.ID {
		t.Fatal("authorized replacement not retained")
	}
	timePost(t, router, cookie, path, url.Values{"job_id": {""}, "reason": {"Clear before archive selection check"}}, 303)
	client.Job.UpdateOneID(eligible.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	response := timeHTTP(router, cookie, path, timeFormValues(t, router, cookie, path, url.Values{"job_id": {fmt.Sprint(eligible.ID)}, "reason": {"Must reject new archived link"}}))
	if response.Code != 403 && response.Code != 404 {
		t.Fatalf("archived selection=%d %s", response.Code, response.Body)
	}
}

func TestTimeFutureAndClosedIntervalValidationHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("validation original").SaveX(ctx)
	future := time.Now().Add(24 * time.Hour).UTC().Truncate(time.Microsecond)
	for _, interval := range [][2]string{{"2026-01-02T10:00:00Z", "2026-01-02T10:00:00Z"}, {"2026-01-02T11:00:00Z", "2026-01-02T10:00:00Z"}, {"2026-01-02T10:00:00Z", future.Format(time.RFC3339Nano)}} {
		timePost(t, router, cookie, "/time-entries", url.Values{"user_id": {fmt.Sprint(admin)}, "clock_in": {interval[0]}, "clock_out": {interval[1]}}, 409)
	}
	path := fmt.Sprintf("/time-entries/%d", entry.ID)
	timePost(t, router, cookie, path, url.Values{"clock_in": {future.Format(time.RFC3339Nano)}, "clock_out": {future.Add(time.Hour).Format(time.RFC3339Nano)}, "reason": {"Future proposal must reject"}}, 409)
	timePost(t, router, cookie, path, url.Values{"clock_out": {future.Format(time.RFC3339Nano)}, "reason": {"Future end must reject"}}, 409)
	if client.TimeEntry.Query().CountX(ctx) != 1 || !client.TimeEntry.GetX(ctx, entry.ID).ClockIn.Equal(start) || !client.TimeEntry.GetX(ctx, entry.ID).ClockOut.Equal(start.Add(time.Hour)) {
		t.Fatal("invalid interval partially persisted")
	}
}

func TestTimeVoidTerminalActiveHTTP(t *testing.T) {
	_, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	itemPost(t, router, cookie, "/time-entries/clock-in", nil, 303)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	path := fmt.Sprintf("/time-entries/%d", entry.ID)
	timePost(t, router, cookie, path+"/void", url.Values{"reason": {"Mistaken clock"}}, 303)
	if client.TimeEntry.GetX(ctx, entry.ID).ClockOut != nil {
		t.Fatal("Void fabricated clock-out")
	}
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Voided")
	assertContains(t, body, "Mistaken clock")
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/time-entries", 200), fmt.Sprintf("/time-entries/%d\"", entry.ID))
	timePost(t, router, cookie, path, url.Values{"notes": {"unvoid attempt"}, "reason": {"cannot correct void"}}, 409)
	timePost(t, router, cookie, path+"/void", url.Values{"reason": {"second void"}}, 409)
	itemPost(t, router, cookie, "/time-entries/clock-out", url.Values{"entry_id": {fmt.Sprint(entry.ID)}}, 409)
	itemPost(t, router, cookie, "/time-entries/clock-in", nil, 303)
	newer := client.TimeEntry.Query().Where(timeentry.IDNEQ(entry.ID)).OnlyX(ctx)
	if newer.ClockOut != nil {
		t.Fatal("replacement clock was not active")
	}
}

func TestTimeCorrectionRetainsBeforeAfterHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("original immutable notes").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/time-entries/%d", entry.ID)
	form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	values := url.Values{"notes": {"corrected notes"}, "reason": {"Reviewed paper record"}, "save_action_id": {compositionField(t, form, "save_action_id")}, "save_revision": {compositionField(t, form, "save_revision")}, "save_history": {compositionField(t, form, "save_history")}}
	itemPost(t, router, cookie, path, values, 303)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "original immutable notes")
	assertContains(t, body, "corrected notes")
	assertContains(t, body, "Reviewed paper record")
	assertContains(t, body, "Before")
	assertContains(t, body, "After")
	// Reconciliation of the same browser identity must not create another event.
	itemPost(t, router, cookie, path, values, 409)
	if after := requestBody(t, router, cookie, "GET", path, 200); strings.Count(after, "Reviewed paper record") != strings.Count(body, "Reviewed paper record") {
		t.Fatal("replay duplicated history")
	}
}

func TestTimeManualRepairLifecycleHTTP(t *testing.T) {
	_, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	form := requestBody(t, router, cookie, "GET", "/time-entries/new", 200)
	assertContains(t, form, "Closed manual Time Entry")
	values := url.Values{"user_id": {fmt.Sprint(admin)}, "clock_in": {"2026-01-01T10:00:00Z"}, "clock_out": {"2026-01-01T11:00:00Z"}, "notes": {"missed closed time"}}
	timePost(t, router, cookie, "/time-entries", values, 303)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	path := fmt.Sprintf("/time-entries/%d", entry.ID)
	timePost(t, router, cookie, "/time-entries", values, 409)
	values.Set("clock_out", "")
	timePost(t, router, cookie, "/time-entries", values, 409)
	timePost(t, router, cookie, path, url.Values{"clock_out": {""}, "notes": {""}, "reason": {"Forgot to keep clock running"}}, 303)
	current := client.TimeEntry.GetX(ctx, entry.ID)
	if current.ClockOut != nil || current.Notes != "" || current.UserID != admin {
		t.Fatalf("reopening/clear changed wrong facts: %+v", current)
	}
	itemPost(t, router, cookie, "/time-entries/clock-in", nil, 409)
	timePost(t, router, cookie, path+"/void", url.Values{"reason": {"Replace erroneous interval"}}, 303)
	values.Set("clock_out", "2026-01-01T11:00:00Z")
	timePost(t, router, cookie, "/time-entries", values, 303)
	if client.TimeEntry.Query().CountX(ctx) != 2 {
		t.Fatal("repair did not retain original identity plus replacement")
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Forgot to keep clock running")
}

func TestTimeCorrectionFaultIsRetryableHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("before dependency fault").SaveX(ctx)
	path := fmt.Sprintf("/time-entries/%d", entry.ID)
	form := requestBody(t, router, cookie, "GET", path+"/edit", 200)
	values := url.Values{"notes": {"after dependency recovery"}, "reason": {"Reviewed correction"}, "save_action_id": {compositionField(t, form, "save_action_id")}, "save_revision": {compositionField(t, form, "save_revision")}, "save_history": {compositionField(t, form, "save_history")}}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_time_history() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected history failure'; END $$; CREATE TRIGGER reject_time_history BEFORE INSERT ON time_entry_history FOR EACH ROW EXECUTE FUNCTION reject_time_history()`); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, path, values, 503)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "before dependency fault")
	assertNotContains(t, body, "after dependency recovery")
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_time_history ON time_entry_history`); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, path, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "after dependency recovery")
}
