package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestTimesheetsFilterRetentionFailureAndAPIBoundaryHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	worker := client.User.Create().SetCompanyID(company).SetEmail("report-own-filter@test").SetName("Own filter reader").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	a := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, a, 0, "Report own filter", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, a, worker.ID, role); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("HIDDEN REPORT FACT").SaveX(ctx)
	own := client.TimeEntry.Create().SetCompanyID(company).SetUserID(worker.ID).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("Own filter fact").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, worker.ID)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/time-entries?user_id=%d", admin), 200)
	assertNotContains(t, body, "HIDDEN REPORT FACT")
	assertNotContains(t, body, "Own filter fact")
	assertContains(t, body, fmt.Sprintf(`value="%d" selected`, admin))
	body = requestBody(t, router, cookie, "GET", "/time-entries?job_id=999999", 200)
	assertContains(t, body, `value="999999" selected`)
	assertNotContains(t, body, "Own filter fact")
	if _, err := pool.Exec(ctx, `ALTER TABLE time_entry_history RENAME TO timesheet_unavailable_history`); err != nil {
		t.Fatal(err)
	}
	restored := false
	t.Cleanup(func() {
		if !restored {
			_, _ = pool.Exec(context.Background(), `ALTER TABLE timesheet_unavailable_history RENAME TO time_entry_history`)
		}
	})
	path := "/time-entries?date_from=2026-01-01&source=clocked&group_by=job"
	body = requestBody(t, router, cookie, "GET", path, 503)
	for _, v := range []string{"data-timesheet-total", "Own filter fact", "HIDDEN REPORT FACT", "timesheet_unavailable_history", "SQLSTATE"} {
		assertNotContains(t, body, v)
	}
	assertContains(t, body, "Retry")
	assertContains(t, body, "date_from=2026-01-01")
	if _, err := pool.Exec(ctx, `ALTER TABLE timesheet_unavailable_history RENAME TO time_entry_history`); err != nil {
		t.Fatal(err)
	}
	restored = true
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Own filter fact")
	token, err := sessions.CreateMobile(ctx, admin, "Timesheet API exclusion")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	for _, target := range []string{"/time-entries?group_by=job", "/timesheets", fmt.Sprintf("/time-entries/%d", own.ID)} {
		code, body := clockReviewCall(t, server, token, "GET", target, nil)
		if code != 404 && code != 405 {
			t.Fatalf("report API expanded: %s status=%d %s", target, code, body)
		}
		assertNotContains(t, body, "Own filter fact")
	}
}
