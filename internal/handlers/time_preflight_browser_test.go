package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestTimeManualPreflightRepairPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	output, err := exec.Command("node", "testdata/browser/time-preflight.cjs", server.URL, sessionCookie(t, context.Background(), sessions, admin).Value).CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}

func TestTimeCorrectionPreflightRepairPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("browser original correction facts").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	output, err := exec.Command("node", "testdata/browser/time-correction-preflight.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, fmt.Sprint(entry.ID)).CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
	current := client.TimeEntry.GetX(ctx, entry.ID)
	if current.Notes != "browser retained correction draft" || !current.ClockIn.Equal(start) || current.ClockOut == nil || !current.ClockOut.Equal(*entry.ClockOut) || current.UserID != admin || client.TimeEntry.Query().CountX(ctx) != 1 {
		t.Fatal("browser correction did not preserve original identity and interval")
	}
	var history, audit, effects int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM time_entry_history),(SELECT count(*) FROM authorization_audit WHERE target_type='time_entry'),(SELECT count(*) FROM online_save_effects)`).Scan(&history, &audit, &effects); err != nil || history != 1 || audit != 1 || effects != 1 {
		t.Fatalf("browser correction history/audit/effects=%d/%d/%d err=%v", history, audit, effects, err)
	}
}
