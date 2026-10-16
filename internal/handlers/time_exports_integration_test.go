package handlers

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
)

func TestTimeExportExactSnapshotHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York'`); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 3, 8, 4, 30, 0, 123000, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(time.Date(2026, 3, 9, 4, 30, 0, 124000, time.UTC)).SetNotes("Exact export").SaveX(ctx)
	review := requestBody(t, router, cookie, "GET", "/time-entries/export?group_by=subject", 200)
	assertContains(t, review, "All matching")
	values := url.Values{"operation_id": {newOperationKey()}, "group_by": {"subject"}, "scope": {"A"}}
	path := masterPost(t, router, cookie, "/time-entries/export", values, 303)
	if replay := masterPost(t, router, cookie, "/time-entries/export", values, 303); replay != path {
		t.Fatal("duplicate accepted export")
	}
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("expected three exact company-day rows: %v", rows)
	}
	for _, want := range []string{fmt.Sprint(entry.ID), "2026-03-07", "2026-03-08", "2026-03-09", "82800000000", "Exact export"} {
		// The complete short day is exactly 23 hours; fractional endpoints remain in adjacent days.
		assertContains(t, body, want)
	}
	for _, forbidden := range []string{"latitude", "longitude", "captured_at", "accuracy"} {
		assertNotContains(t, body, forbidden)
	}
}

func TestTimeExportIndependentGPSHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	captured := time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	itemPost(t, router, adminCookie, "/time-entries/clock-in", url.Values{"latitude": {"12.3456789"}, "longitude": {"98.7654321"}, "accuracy": {"17.25"}, "captured_at": {captured}}, 303)
	entry := client.TimeEntry.Query().OnlyX(ctx)
	reader := client.User.Create().SetCompanyID(company).SetEmail("gps-export@test").SetName("GPS exporter").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	a := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.gps.view", Scope: authorization.All}, {Permission: "time.gps.export", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, a, 0, "GPS independent", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, a, reader.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, reader.ID)
	requestBody(t, router, cookie, "GET", "/time-entries/export?type=gps&scope=A", 200)
	path := masterPost(t, router, cookie, "/time-entries/export", url.Values{"operation_id": {newOperationKey()}, "type": {"gps"}, "scope": {"A"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path+"/download", 200)
	for _, s := range []string{"latitude", "longitude", "captured_at", "accuracy", "12.3456789", "98.7654321", captured, "17.25"} {
		assertContains(t, body, s)
	}
	for _, s := range []string{"duration_microseconds", "revisions", "notes"} {
		assertNotContains(t, body, s)
	}
	expectStatus(t, router, cookie, "GET", "/time-entries/export?scope=A", 403)
	correctedStart := time.Now().Add(-5 * time.Minute).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	timePost(t, router, adminCookie, fmt.Sprintf("/time-entries/%d", entry.ID), url.Values{"clock_in": {correctedStart}, "reason": {"Review clock interval, retain GPS"}}, 303)
	// Correction and Void preserve accepted observations, including the original capture time.
	timePost(t, router, adminCookie, fmt.Sprintf("/time-entries/%d/void", entry.ID), url.Values{"reason": {"GPS retained after Void"}}, 303)
	path2 := masterPost(t, router, cookie, "/time-entries/export", url.Values{"operation_id": {newOperationKey()}, "type": {"gps"}, "scope": {"A"}, "include_void": {"true"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", path2+"/download", 200), captured)
	// A separately authorized time artifact survives GPS authority loss.
	grants = append(grants, authorization.Grant{Permission: "time.export", Scope: authorization.All})
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, a, role, "GPS independent", grants, revision); err != nil {
		t.Fatal(err)
	}
	timePath := masterPost(t, router, cookie, "/time-entries/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}, "include_void": {"true"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, a, role, "GPS independent", []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.export", Scope: authorization.All}}, revision+1); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path+"/download", 410)
	expectStatus(t, router, cookie, "GET", path2+"/download", 410)
	timeBody := requestBody(t, router, cookie, "GET", timePath+"/download", 200)
	assertContains(t, timeBody, "GPS retained after Void")
	assertNotContains(t, timeBody, "12.3456789")
}
