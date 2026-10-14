package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestClockInvalidJobTargetsNeverBecomeStandaloneHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	post := func(path string, values url.Values) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	for _, path := range []string{"/jobs/0/clock-in", "/jobs/-1/clock-in", "/jobs/999999/clock-in"} {
		code := post(path, nil)
		count := client.TimeEntry.Query().CountX(ctx)
		if code != 404 || count != 0 {
			t.Fatalf("invalid Job path became a standalone clock: code=%d entries=%d path=%s", code, count, path)
		}
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Explicit clock target").SaveX(ctx)
	first := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("First target").SaveX(ctx)
	second := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Second target").SaveX(ctx)
	inPath := fmt.Sprintf("/jobs/%d/clock-in", first.ID)
	if code := post(inPath, url.Values{"job_id": {fmt.Sprint(second.ID)}}); code != 400 || client.TimeEntry.Query().CountX(ctx) != 0 {
		t.Fatalf("conflicting in target was guessed: code=%d", code)
	}
	if code := post(inPath, nil); code != 303 {
		t.Fatalf("valid target=%d", code)
	}
	entry := client.TimeEntry.Query().OnlyX(ctx)
	if code := post("/jobs/0/clock-out", url.Values{"entry_id": {fmt.Sprint(entry.ID)}}); code != 404 {
		t.Fatalf("invalid out route=%d", code)
	}
	outPath := fmt.Sprintf("/jobs/%d/clock-out", first.ID)
	if code := post(outPath, url.Values{"entry_id": {fmt.Sprint(entry.ID)}, "job_id": {fmt.Sprint(second.ID)}}); code != 400 {
		t.Fatalf("conflicting out target=%d", code)
	}
	if client.TimeEntry.GetX(ctx, entry.ID).ClockOut != nil {
		t.Fatal("invalid target closed active entry")
	}
	if code := post(outPath, url.Values{"entry_id": {fmt.Sprint(entry.ID)}}); code != 303 {
		t.Fatalf("valid exact out=%d", code)
	}
}
