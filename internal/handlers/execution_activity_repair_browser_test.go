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

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestExecutionActivityRepairPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser execution history owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser execution history job").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Browser occurrence device")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", apiv1.NewRouter(pool, client, sessions)))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/execution-activity-repair.cjs", server.URL, cookie.Value, fmt.Sprint(j.ID), token).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type='job' AND object_id=$1 AND action IN ('field_notes_updated','status_transitioned','subtask_added','subtask_renamed','subtasks_reordered','subtask_completed','subtask_uncompleted','subtask_removed')`, j.ID).Scan(&count); err != nil || count != 10 {
		t.Fatalf("persisted browser events=%d err=%v", count, err)
	}
}
