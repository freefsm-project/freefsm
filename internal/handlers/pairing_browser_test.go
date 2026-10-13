package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/go-chi/chi/v5"
)

func TestPairingPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := chi.NewRouter()
	mux.Mount("/api/v1", apiv1.NewRouter(pool, client, sessions))
	mux.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Mount("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "testdata/browser/pairing.cjs", server.URL)
	cmd.Env = append(os.Environ(), "PAIRING_BROWSER_COOKIE="+cookie.Value)
	output, err := cmd.CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	var issued, revoked int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(revoked_at) FROM sessions WHERE kind='mobile' AND device_name='Browser protocol tablet'`).Scan(&issued, &revoked); err != nil {
		t.Fatal(err)
	}
	if issued != 1 || revoked != 1 {
		t.Fatalf("persisted browser effects issued=%d revoked=%d", issued, revoked)
	}
}
