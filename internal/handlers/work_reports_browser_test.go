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
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestWorkReportsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser report Customer").SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser workload fact").SaveX(ctx)
	client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Browser cohort fact").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Browser report reader").SetEmail("work-browser@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser report read only", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "estimate.view", Scope: authorization.All}})
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	cmd := exec.CommandContext(ctx, "node", "testdata/browser/work-reports.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, sessionCookie(t, ctx, sessions, u.ID).Value)
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	historyCheck(t, err)
}
