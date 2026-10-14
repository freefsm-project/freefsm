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

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestChecklistUIRepairBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, actor, 0, "UI checklist maintainer", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.subtask.manage", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("UI maintainer").SetEmail("ui-checklist@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("UI owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("UI checklist").SaveX(ctx)
	mux := http.NewServeMux()
	// Test control changes real authority; subsequent browser requests still use
	// the production router and session, including receipt non-disclosure.
	mux.HandleFunc("POST /test-ui-revoke", func(w http.ResponseWriter, r *http.Request) {
		if _, err := auth.SaveRole(r.Context(), actor, role, "UI checklist maintainer", nil, 1); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	run, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(run, "node", "testdata/browser/checklist-ui-repair.cjs", server.URL, sessionCookie(t, ctx, sessions, u.ID).Value, fmt.Sprint(j.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
