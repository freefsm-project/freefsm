package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

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
