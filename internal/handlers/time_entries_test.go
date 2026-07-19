package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
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
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()

	ctx := context.Background()
	const companyID int64 = 1
	client.CompanySettings.Create().SetCompanyID(companyID).SetBusinessName("Timesheet Company").SetTimezone("America/New_York").SaveX(ctx)
	admin := client.User.Create().SetCompanyID(companyID).SetEmail("timesheet-admin@example.test").SetPasswordHash("hash").SetName("Admin").SetRole("admin").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(admin.ID).SetClockIn(time.Date(2026, time.July, 10, 3, 59, 59, 0, time.UTC)).SetClockOut(time.Date(2026, time.July, 10, 4, 59, 59, 0, time.UTC)).SetNotes("previous local day").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(admin.ID).SetClockIn(time.Date(2026, time.July, 10, 4, 0, 0, 0, time.UTC)).SetClockOut(time.Date(2026, time.July, 10, 5, 0, 0, 0, time.UTC)).SetNotes("local day start").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(admin.ID).SetClockIn(time.Date(2026, time.July, 11, 3, 59, 59, 0, time.UTC)).SetClockOut(time.Date(2026, time.July, 11, 4, 59, 59, 0, time.UTC)).SetNotes("local day end").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(admin.ID).SetClockIn(time.Date(2026, time.July, 11, 4, 0, 0, 0, time.UTC)).SetClockOut(time.Date(2026, time.July, 11, 5, 0, 0, 0, time.UTC)).SetNotes("next local day").SaveX(ctx)

	body := serveTimeEntryList(t, client, admin, "/time-entries?date_from=2026-07-10&date_to=2026-07-10", http.StatusOK)
	assertContains(t, body, "local day start")
	assertContains(t, body, "local day end")
	assertNotContains(t, body, "previous local day")
	assertNotContains(t, body, "next local day")
	assertContains(t, body, `name="date_from" value="2026-07-10"`)
	assertContains(t, body, `name="date_to" value="2026-07-10"`)
}

func TestTimeEntryListRejectsInvalidDateRanges(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()

	ctx := context.Background()
	const companyID int64 = 1
	client.CompanySettings.Create().SetCompanyID(companyID).SetBusinessName("Timesheet Company").SetTimezone("UTC").SaveX(ctx)
	admin := client.User.Create().SetCompanyID(companyID).SetEmail("range-admin@example.test").SetPasswordHash("hash").SetName("Admin").SetRole("admin").SaveX(ctx)

	for _, path := range []string{
		"/time-entries?date_from=not-a-date",
		"/time-entries?date_to=2026-02-30",
		"/time-entries?date_from=2026-07-11&date_to=2026-07-10",
	} {
		t.Run(path, func(t *testing.T) {
			serveTimeEntryList(t, client, admin, path, http.StatusBadRequest)
		})
	}
}

func TestTimeEntryListEnforcesRoleScopeAndCombinesOfficeFilters(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()

	ctx := context.Background()
	const companyID int64 = 1
	client.CompanySettings.Create().SetCompanyID(companyID).SetBusinessName("Timesheet Company").SetTimezone("UTC").SaveX(ctx)
	admin := client.User.Create().SetCompanyID(companyID).SetEmail("scope-admin@example.test").SetPasswordHash("hash").SetName("Admin").SetRole("admin").SaveX(ctx)
	tech := client.User.Create().SetCompanyID(companyID).SetEmail("scope-tech@example.test").SetPasswordHash("hash").SetName("Tech").SetRole("tech").SaveX(ctx)
	other := client.User.Create().SetCompanyID(companyID).SetEmail("scope-other@example.test").SetPasswordHash("hash").SetName("Other").SetRole("tech").SaveX(ctx)
	clockIn := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	client.TimeEntry.Create().SetUserID(tech.ID).SetClockIn(clockIn).SetClockOut(clockIn.Add(time.Hour)).SetNotes("repair self result").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(other.ID).SetClockIn(clockIn).SetClockOut(clockIn.Add(time.Hour)).SetNotes("repair selected result").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(other.ID).SetClockIn(clockIn.AddDate(0, 0, 1)).SetClockOut(clockIn.AddDate(0, 0, 1).Add(time.Hour)).SetNotes("repair wrong date").SaveX(ctx)
	client.TimeEntry.Create().SetUserID(other.ID).SetClockIn(clockIn).SetClockOut(clockIn.Add(time.Hour)).SetNotes("inspection wrong search").SaveX(ctx)

	techBody := serveTimeEntryList(t, client, tech, fmt.Sprintf("/time-entries?user_id=%d", other.ID), http.StatusOK)
	assertContains(t, techBody, "repair self result")
	assertNotContains(t, techBody, "repair selected result")

	officePath := fmt.Sprintf("/time-entries?user_id=%d&search=repair&date_from=2026-07-10&date_to=2026-07-10", other.ID)
	officeBody := serveTimeEntryList(t, client, admin, officePath, http.StatusOK)
	assertContains(t, officeBody, "repair selected result")
	assertNotContains(t, officeBody, "repair self result")
	assertNotContains(t, officeBody, "repair wrong date")
	assertNotContains(t, officeBody, "inspection wrong search")
	assertContains(t, officeBody, fmt.Sprintf(`value="%d" selected`, other.ID))
}

func serveTimeEntryList(t *testing.T, client *ent.Client, user *ent.User, target string, wantStatus int) string {
	t.Helper()
	h := NewTimeEntryHandler(
		services.NewTimeEntryService(client),
		services.NewUserService(client),
		services.NewJobService(client),
		nil,
	)
	handler := middleware.Company(services.NewCompanySettingsService(client))(http.HandlerFunc(h.List))
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserKey, &middleware.UserInfo{
		ID: user.ID, Name: user.Name, Role: user.Role, CompanyID: valueInt64(user.CompanyID),
	}))
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != wantStatus {
		t.Fatalf("status=%d, want %d; body=%s", rr.Code, wantStatus, rr.Body.String())
	}
	return rr.Body.String()
}
