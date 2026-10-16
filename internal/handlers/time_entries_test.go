package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestParseTimeEntryDateRange(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}

	from, before, err := parseTimeEntryDateRange("2026-07-10", "2026-07-10", location)
	if err != nil {
		t.Fatalf("parseTimeEntryDateRange: %v", err)
	}
	wantFrom := time.Date(2026, time.July, 10, 4, 0, 0, 0, time.UTC)
	wantBefore := time.Date(2026, time.July, 11, 4, 0, 0, 0, time.UTC)
	if from == nil || !from.Equal(wantFrom) {
		t.Fatalf("from=%v, want %v", from, wantFrom)
	}
	if before == nil || !before.Equal(wantBefore) {
		t.Fatalf("before=%v, want %v", before, wantBefore)
	}

	for _, tc := range []struct {
		name     string
		dateFrom string
		dateTo   string
	}{
		{name: "invalid from", dateFrom: "July 10"},
		{name: "invalid to", dateTo: "2026-02-30"},
		{name: "reversed", dateFrom: "2026-07-11", dateTo: "2026-07-10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseTimeEntryDateRange(tc.dateFrom, tc.dateTo, location); err == nil {
				t.Fatal("parseTimeEntryDateRange returned nil error")
			}
		})
	}
}

func TestTimeEntryListRequiresAuthenticatedUser(t *testing.T) {
	h := &TimeEntryHandler{}
	req := httptest.NewRequest(http.MethodGet, "/time-entries", nil)
	rr := httptest.NewRecorder()

	h.List(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestTimeEntryListDateFilterUsesCompanyTimezoneAndRetainsValues(t *testing.T) {
	pool, client, router, sessions, companyID, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York'`); err != nil {
		t.Fatal(err)
	}
	client.TimeEntry.Create().SetCompanyID(companyID).SetUserID(admin).SetClockIn(time.Date(2026, time.July, 10, 3, 59, 59, 0, time.UTC)).SetClockOut(time.Date(2026, time.July, 10, 4, 59, 59, 0, time.UTC)).SetNotes("previous local day").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(companyID).SetUserID(admin).SetClockIn(time.Date(2026, time.July, 10, 4, 0, 0, 0, time.UTC)).SetClockOut(time.Date(2026, time.July, 10, 5, 0, 0, 0, time.UTC)).SetNotes("local day start").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(companyID).SetUserID(admin).SetClockIn(time.Date(2026, time.July, 11, 3, 59, 59, 0, time.UTC)).SetClockOut(time.Date(2026, time.July, 11, 4, 59, 59, 0, time.UTC)).SetNotes("local day end").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(companyID).SetUserID(admin).SetClockIn(time.Date(2026, time.July, 11, 4, 0, 0, 0, time.UTC)).SetClockOut(time.Date(2026, time.July, 11, 5, 0, 0, 0, time.UTC)).SetNotes("next local day").SaveX(ctx)

	body := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", "/time-entries?date_from=2026-07-10&date_to=2026-07-10", http.StatusOK)
	assertContains(t, body, "local day start")
	assertContains(t, body, "local day end")
	// Timesheets include the intersecting part of entries begun on an earlier
	// company day, rather than filtering solely by the clock-in date.
	assertContains(t, body, "previous local day")
	assertNotContains(t, body, "next local day")
	assertContains(t, body, `name="date_from" value="2026-07-10"`)
	assertContains(t, body, `name="date_to" value="2026-07-10"`)
}

func TestTimeEntryListRejectsInvalidDateRanges(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()

	for _, path := range []string{
		"/time-entries?date_from=not-a-date",
		"/time-entries?date_to=2026-02-30",
		"/time-entries?date_from=2026-07-11&date_to=2026-07-10",
	} {
		t.Run(path, func(t *testing.T) {
			expectStatus(t, router, sessionCookie(t, ctx, sessions, admin), "GET", path, http.StatusBadRequest)
		})
	}
}

func TestTimeEntryListEnforcesGrantScopeAndCombinesAuthorizedFilters(t *testing.T) {
	pool, client, router, sessions, companyID, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='UTC'`); err != nil {
		t.Fatal(err)
	}
	tech := client.User.Create().SetCompanyID(companyID).SetEmail("scope-tech@example.test").SetPasswordHash("hash").SetName("Tech").SetRole("tech").SaveX(ctx)
	other := client.User.Create().SetCompanyID(companyID).SetEmail("scope-other@example.test").SetPasswordHash("hash").SetName("Other").SetRole("tech").SaveX(ctx)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Own timesheet", []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, tech.ID, role); err != nil {
		t.Fatal(err)
	}
	clockIn := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	client.TimeEntry.Create().SetCompanyID(companyID).SetUserID(tech.ID).SetClockIn(clockIn).SetClockOut(clockIn.Add(time.Hour)).SetNotes("repair self result").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(companyID).SetUserID(other.ID).SetClockIn(clockIn).SetClockOut(clockIn.Add(time.Hour)).SetNotes("repair selected result").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(companyID).SetUserID(other.ID).SetClockIn(clockIn.AddDate(0, 0, 1)).SetClockOut(clockIn.AddDate(0, 0, 1).Add(time.Hour)).SetNotes("repair wrong date").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(companyID).SetUserID(other.ID).SetClockIn(clockIn).SetClockOut(clockIn.Add(time.Hour)).SetNotes("inspection wrong search").SaveX(ctx)

	techBody := requestBody(t, router, sessionCookie(t, ctx, sessions, tech.ID), "GET", fmt.Sprintf("/time-entries?user_id=%d", other.ID), http.StatusOK)
	// A supplied subject filter is retained, not silently replaced with Own.
	assertNotContains(t, techBody, "repair self result")
	assertNotContains(t, techBody, "repair selected result")

	officePath := fmt.Sprintf("/time-entries?user_id=%d&search=repair&date_from=2026-07-10&date_to=2026-07-10", other.ID)
	officeBody := requestBody(t, router, sessionCookie(t, ctx, sessions, admin), "GET", officePath, http.StatusOK)
	assertContains(t, officeBody, "repair selected result")
	assertNotContains(t, officeBody, "repair self result")
	assertNotContains(t, officeBody, "repair wrong date")
	assertNotContains(t, officeBody, "inspection wrong search")
	assertContains(t, officeBody, fmt.Sprintf(`value="%d" selected`, other.ID))
}
