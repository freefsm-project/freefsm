package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestSharedGuardedSavePersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Shared interaction parent").SaveX(context.Background())
	authority := authorization.New(pool)
	role, err := authority.SaveRole(context.Background(), authorization.Actor{UserID: admin}, 0, "Shared editor", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.edit", Scope: authorization.All}, {Permission: "item.view", Scope: authorization.All}, {Permission: "item.edit", Scope: authorization.All}, {Permission: "cost.view", Scope: authorization.All}, {Permission: "cost.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	editor := client.User.Create().SetCompanyID(company).SetEmail("shared-editor@test").SetName("Shared editor").SetPasswordHash("hash").SetRole("tech").SaveX(context.Background())
	if err := authority.AssignUser(context.Background(), authorization.Actor{UserID: admin}, editor.ID, role); err != nil {
		t.Fatal(err)
	}
	editorCookie := sessionCookie(t, context.Background(), sessions, editor.ID)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	reauthenticatedCookie := sessionCookie(t, context.Background(), sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	web := middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router))))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.Header.Get("X-Test-Lose-Response") == "true" {
			buffer := httptest.NewRecorder()
			web.ServeHTTP(buffer, r)
			if buffer.Code >= 400 && !(buffer.Code == 409 && strings.Contains(buffer.Body.String(), `"disposition":"saved"`)) {
				t.Errorf("lost response must follow accepted command: %d %s", buffer.Code, buffer.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		web.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/shared-save.cjs", server.URL, cookie.Value, fmt.Sprint(customer.ID), editorCookie.Value, fmt.Sprint(role), reauthenticatedCookie.Value).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
