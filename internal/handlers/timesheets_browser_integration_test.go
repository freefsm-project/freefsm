package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestTimesheetsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York'`); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Report Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Browser report Job").SaveX(ctx)
	start := time.Date(2026, 3, 8, 4, 30, 0, 0, time.UTC)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetJobID(job.ID).SetClockIn(start).SetClockOut(start.Add(24 * time.Hour)).SetIsManual(true).SetNotes("Browser exact day row").SaveX(ctx)
	start = time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("Browser clocked source row").SaveX(ctx)
	denied := client.User.Create().SetCompanyID(company).SetEmail("report-browser-denied@test").SetName("Denied browser report").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	a := authorization.Actor{UserID: admin}
	empty, err := auth.SaveRole(ctx, a, 0, "No browser report", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, a, denied.ID, empty); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	evidence := os.Getenv("TIMESHEET_BROWSER_EVIDENCE")
	if evidence == "" {
		evidence = t.TempDir()
	} else if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("node", "testdata/browser/timesheets.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, sessionCookie(t, ctx, sessions, denied.ID).Value, evidence).CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}
