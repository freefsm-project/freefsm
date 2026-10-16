package handlers

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/timeentry"
)

func TestTimesheetsExactCompanyDaysHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York'`); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 3, 8, 4, 30, 0, 123000, time.UTC)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(time.Date(2026, 3, 9, 4, 30, 0, 124000, time.UTC)).SetNotes("DST exact report").SaveX(ctx)
	body := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", "/time-entries?date_from=2026-03-07&date_to=2026-03-09&group_by=subject", 200)
	for _, expected := range []string{"DST exact report", "23h0m0s", "29m59.999877s", "30m0.000124s", "24h0m0.000001s", "Updated at"} {
		assertContains(t, body, expected)
	}
	body = requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", "/time-entries?date_from=2026-03-08&date_to=2026-03-08", 200)
	assertContains(t, body, "DST exact report")
	assertContains(t, body, "23h0m0s")
	assertNotContains(t, body, "24h0m0.000001s")
}

func TestTimesheetsCurrentHistoryVoidAndGPSHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	timePost(t, router, cookie, "/time-entries", url.Values{"user_id": {fmt.Sprint(admin)}, "clock_in": {"2026-01-01T10:00:00Z"}, "clock_out": {"2026-01-01T11:00:00Z"}, "notes": {"Original report fact"}}, 303)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	timePost(t, router, cookie, fmt.Sprintf("/time-entries/%d", entry.ID), url.Values{"clock_out": {"2026-01-01T12:00:00Z"}, "notes": {"Corrected report fact"}, "reason": {"Reviewed report correction"}}, 303)
	body := requestBody(t, router, cookie, "GET", "/time-entries", 200)
	for _, v := range []string{"Original report fact", "Corrected report fact", "Reviewed report correction", "2h0m0s"} {
		assertContains(t, body, v)
	}
	captured := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	itemPost(t, router, cookie, "/time-entries/clock-in", url.Values{"latitude": {"12.3456789"}, "longitude": {"98.7654321"}, "accuracy": {"17.25"}, "captured_at": {captured}}, 303)
	active := client.TimeEntry.Query().Where(timeentry.ClockOutIsNil()).OnlyX(ctx)
	body = requestBody(t, router, cookie, "GET", "/time-entries?lifecycle=active", 200)
	assertContains(t, body, "Provisional as of")
	assertContains(t, body, "12.3456789")
	assertContains(t, body, "17.25")
	assertNotContains(t, body, "Original report fact")
	reader := client.User.Create().SetCompanyID(company).SetEmail("report-no-gps@test").SetName("Time without location").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	a := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, a, 0, "Time without location", []authorization.Grant{{Permission: "time.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, a, reader.ID, role); err != nil {
		t.Fatal(err)
	}
	denied := requestBody(t, router, sessionCookie(t, ctx, sessions, reader.ID), "GET", "/time-entries?group_by=job", 200)
	for _, v := range []string{"12.3456789", "98.7654321", "17.25", captured} {
		assertNotContains(t, denied, v)
	}
	timePost(t, router, cookie, fmt.Sprintf("/time-entries/%d/void", active.ID), url.Values{"reason": {"Terminal report mistake"}}, 303)
	body = requestBody(t, router, cookie, "GET", "/time-entries?include_void=true", 200)
	for _, v := range []string{"Terminal report mistake", "No recorded end", "Excluded (Void)", "2h0m0s", "12.3456789"} {
		assertContains(t, body, v)
	}
	if strings.Contains(body, "Provisional as of") {
		t.Fatal("void retained an active effect")
	}
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/time-entries", 200), "Terminal report mistake")
}

func TestTimesheetsScopedFiltersAndGroupsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	worker := client.User.Create().SetCompanyID(company).SetEmail("report-worker@test").SetName("Report worker").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	other := client.User.Create().SetCompanyID(company).SetEmail("report-other@test").SetName("Report other").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	grants := []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.view", Scope: authorization.Assigned}, {Permission: "job.view", Scope: authorization.Assigned}}
	role, err := auth.SaveRole(ctx, actor, 0, "Report readers", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, actor, worker.ID, role); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("RESTRICTED CUSTOMER").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Assigned report Job").SaveX(ctx)
	lost := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Retained report Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(worker.ID).SaveX(ctx)
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(worker.ID).SetJobID(lost.ID).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("Own retained row").SaveX(ctx)
	client.Job.UpdateOne(lost).SetJobType("RESTRICTED RENAMED JOB").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(other.ID).SetJobID(job.ID).SetClockIn(start).SetClockOut(start.Add(2 * time.Hour)).SetIsManual(true).SetNotes("Assigned manual row").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(other.ID).SetClockIn(start.Add(3 * time.Hour)).SetClockOut(start.Add(7 * time.Hour)).SetNotes("RESTRICTED STANDALONE").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(other.ID).SetJobID(lost.ID).SetClockIn(start.Add(8 * time.Hour)).SetClockOut(start.Add(16 * time.Hour)).SetNotes("RESTRICTED OTHER JOB").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, worker.ID)
	body := requestBody(t, router, cookie, "GET", "/time-entries?group_by=job", 200)
	for _, v := range []string{"Own retained row", "Assigned manual row", "Retained report Job", "3h0m0s", fmt.Sprintf(`data-timesheet-group="job:%d"`, job.ID)} {
		assertContains(t, body, v)
	}
	for _, v := range []string{"RESTRICTED", fmt.Sprintf(`href="/jobs/%d"`, lost.ID)} {
		assertNotContains(t, body, v)
	}
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/time-entries?job_id=%d&source=manual&lifecycle=closed&group_by=subject&user_id=%d", job.ID, other.ID), 200)
	assertContains(t, body, "Assigned manual row")
	assertContains(t, body, "2h0m0s")
	assertNotContains(t, body, "Own retained row")
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/time-entries?job_id=%d&source=clocked", job.ID), 200)
	assertNotContains(t, body, "Assigned manual row")
	for _, q := range []string{"source=unknown", "lifecycle=unknown", "group_by=unknown", "job_id=-1", "job_id=0", "user_id=bad", "include_void=bad", "sort=unknown", "user_id=0"} {
		expectStatus(t, router, cookie, "GET", "/time-entries?"+q, 400)
	}
}

func TestTimesheetsTimezonePresentationAndMidnightTransitionsHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, zone, start, end string
		want                   []string
	}{
		{"fall fold", "America/New_York", "2025-11-02T04:00:00Z", "2025-11-03T05:00:00Z", []string{"2025-11-02T00:00:00-04:00", "2025-11-03T00:00:00-05:00", "25h0m0s"}},
		{"skipped midnight", "America/Santiago", "2025-09-07T03:30:00Z", "2025-09-07T05:00:00Z", []string{"2025-09-06T23:30:00-04:00", "2025-09-07T02:00:00-03:00", "30m0s", "1h0m0s", "1h30m0s"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone=$1`, tc.zone); err != nil {
				t.Fatal(err)
			}
			start, _ := time.Parse(time.RFC3339, tc.start)
			end, _ := time.Parse(time.RFC3339, tc.end)
			entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(end).SetNotes("Timezone invariant fact").SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, admin)
			body := requestBody(t, router, cookie, "GET", "/time-entries", 200)
			for _, v := range tc.want {
				assertContains(t, body, v)
			}
			if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='UTC'`); err != nil {
				t.Fatal(err)
			}
			body = requestBody(t, router, cookie, "GET", "/time-entries", 200)
			for _, v := range []string{tc.start, tc.end, "Timezone invariant fact", fmt.Sprintf(`data-time-entry="%d"`, entry.ID), tc.want[len(tc.want)-1]} {
				assertContains(t, body, v)
			}
		})
	}
}

func TestTimesheetsStableRowsAndActiveAsOfHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	other := client.User.Create().SetCompanyID(company).SetEmail("report-sort@test").SetName("Report sort subject").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	first := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SaveX(ctx)
	second := client.TimeEntry.Create().SetCompanyID(company).SetUserID(other.ID).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SaveX(ctx)
	third := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start.Add(2 * time.Hour)).SetClockOut(start.Add(3 * time.Hour)).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, sort := range []string{"start_asc", "start_desc"} {
		for repeat := 0; repeat < 2; repeat++ {
			body := requestBody(t, router, cookie, "GET", "/time-entries?group_by=job&sort="+sort, 200)
			ids := []int64{first.ID, second.ID, third.ID}
			if sort == "start_desc" {
				ids = []int64{third.ID, first.ID, second.ID}
			}
			previous := -1
			for _, id := range ids {
				at := strings.Index(body, fmt.Sprintf(`data-time-entry="%d"`, id))
				if at <= previous {
					t.Fatalf("unstable %s row order: %v", sort, ids)
				}
				previous = at
			}
			assertContains(t, body, "3h0m0s")
		}
	}
	start = time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	for _, id := range []int64{admin, other.ID} {
		client.TimeEntry.Create().SetCompanyID(company).SetUserID(id).SetClockIn(start).SetNotes("Same provisional start").SaveX(ctx)
	}
	body := requestBody(t, router, cookie, "GET", "/time-entries?lifecycle=active&group_by=job", 200)
	rows := regexp.MustCompile(`(?s)<tr data-time-entry="\d+">.*?<strong>([^<]+)</strong>`).FindAllStringSubmatch(body, -1)
	if len(rows) != 2 || rows[0][1] != rows[1][1] {
		t.Fatalf("active rows did not share one as-of instant: %v", rows)
	}
	assertContains(t, body, "Updated at")
	assertContains(t, body, "Provisional as of")
	assertNotContains(t, body, " Final</span>")
}

func TestTimesheetsDateFilterHandlesMidnightDateRollbackHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/St_Johns'`); err != nil {
		t.Fatal(err)
	}
	// IANA: 1988-10-30 01:31 UTC rolls local 00:01 back to October29 22:01.
	// This entry has 31 minutes on October30 and 119 minutes on October29,
	// even though its original start and end both display as October30.
	start := time.Date(1988, 10, 30, 1, 30, 0, 0, time.UTC)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(150 * time.Minute)).SetNotes("Retained midnight rollback").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, tc := range []struct{ date, total string }{{"1988-10-30", "31m0s"}, {"1988-10-29", "1h59m0s"}} {
		t.Run(tc.date, func(t *testing.T) {
			body := requestBody(t, router, cookie, "GET", "/time-entries?date_from="+tc.date+"&date_to="+tc.date, 200)
			assertContains(t, body, "Retained midnight rollback")
			assertContains(t, body, `data-timesheet-total>`+tc.total+`</strong>`)
		})
	}
}
