package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestTimeExportPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetIsManual(true).SetNotes("Browser exported fact").SaveX(ctx)
	reader := client.User.Create().SetCompanyID(company).SetEmail("export-browser-reader@test").SetName("Report reader").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser read only", []authorization.Grant{{Permission: "time.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, reader.ID, role); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	cmd := exec.Command("node", "testdata/browser/time-exports.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, sessionCookie(t, ctx, sessions, reader.ID).Value)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}

func TestTimeExportRecoveryPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	type journey struct{ Kind, Token, Expired, Failed string }
	var journeys []journey
	for _, kind := range []string{"time", "gps"} {
		user := client.User.Create().SetCompanyID(company).SetEmail(kind + "-recovery-browser@test").SetName(kind + " recovery browser").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
		grants := []authorization.Grant{{Permission: "time.view", Scope: authorization.Own}, {Permission: "time.clock_in", Scope: authorization.Own}}
		permission := "time.export"
		if kind == "gps" {
			permission = "time.gps.export"
			grants = append(grants, authorization.Grant{Permission: "time.gps.view", Scope: authorization.Own}, authorization.Grant{Permission: "time.gps.capture", Scope: authorization.Own})
		}
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.Own})
		role, err := auth.SaveRole(ctx, actor, 0, kind+" browser recovery", grants)
		if err != nil {
			t.Fatal(err)
		}
		if err := auth.AssignUser(ctx, actor, user.ID, role); err != nil {
			t.Fatal(err)
		}
		cookie := sessionCookie(t, ctx, sessions, user.ID)
		clock := url.Values{"notes": {"Recovered browser fact"}}
		if kind == "gps" {
			clock.Set("latitude", "12.5")
			clock.Set("longitude", "23.5")
			clock.Set("captured_at", time.Now().Add(-time.Second).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano))
		}
		itemPost(t, router, cookie, "/time-entries/clock-in", clock, 303)
		expired := prepareTimeExport(t, pool, router, cookie, url.Values{"type": {kind}, "search": {"PRIVATE old filter"}})
		if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1`, strings.TrimPrefix(expired, "/exports/")); err != nil {
			t.Fatal(err)
		}
		failed := failedTimeRecoveryExport(t, pool, router, cookie, kind)
		journeys = append(journeys, journey{kind, cookie.Value, expired, failed})
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	raw, err := json.Marshal(journeys)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", "testdata/browser/time-export-recovery.cjs", server.URL, string(raw))
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(fmt.Errorf("recovery browser: %w", err))
	}
}
