package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestJobArchiveOwnClockPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser clock worker", []authorization.Grant{
		{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "time.view", Scope: authorization.Own},
		{Permission: "time.clock_in", Scope: authorization.Own}, {Permission: "time.clock_out", Scope: authorization.Own}, {Permission: "time.gps.capture", Scope: authorization.Own},
	})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Browser worker").SetEmail("archive-browser@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE CUSTOMER").SaveX(ctx)
	var status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&status))
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetJobType("Historical clock title").SetNotes("PRIVATE PLANNING").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(web)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	output, err := exec.Command("node", "testdata/browser/job-archive-clock.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, sessionCookie(t, ctx, sessions, u.ID).Value, strconv.FormatInt(j.ID, 10), t.TempDir()).CombinedOutput()
	t.Log(string(output))
	historyCheck(t, err)
	var preserved bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT j.deleted_at IS NOT NULL AND e.user_id=$2 AND e.clock_out IS NOT NULL AND (SELECT count(*) FROM time_clock_gps g WHERE g.entry_id=e.id)=1 FROM jobs j JOIN time_entries e ON e.job_id=j.id WHERE j.id=$1`, j.ID, u.ID).Scan(&preserved))
	if !preserved {
		t.Fatal("browser archive/own close-out did not preserve Job, subject and GPS")
	}
}
