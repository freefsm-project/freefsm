package handlers

import (
	"context"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
)

func TestTimeRepairPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	evidence := os.Getenv("TIME_BROWSER_EVIDENCE")
	if evidence == "" {
		evidence = t.TempDir()
	} else if err := os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("node", "testdata/browser/time-repair.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, evidence).CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}
