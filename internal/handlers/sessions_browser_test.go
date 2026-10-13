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

func TestSessionsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	acting := sessionCookie(t, ctx, sessions, admin)
	other := sessionCookie(t, ctx, sessions, admin)
	mobile, err := sessions.CreateMobile(ctx, admin, "Browser field tablet")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	var email string
	if err := pool.QueryRow(ctx, `SELECT email FROM users WHERE id=$1`, admin).Scan(&email); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(runCtx, "node", "testdata/browser/sessions.cjs", server.URL, acting.Value, other.Value, email).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sessions.ValidateMobile(ctx, mobile); err == nil {
		t.Fatal("browser failed to revoke mobile")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE user_id=$1 AND revoked_at IS NOT NULL`, admin).Scan(&count); err != nil || count < 3 {
		t.Fatal(fmt.Sprint("retained revocations ", count, " ", err))
	}
}

func TestSessionReferenceHarnessHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	ordinary, err := sessions.CreateMobile(ctx, admin, "Ordinary reference")
	if err != nil {
		t.Fatal(err)
	}
	restored, err := sessions.CreateMobile(ctx, admin, "Restored reference")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	api := apiv1.NewRouter(pool, client, sessions)
	mux.Handle("/", api)
	// Test-only restore transition. Real encrypted HTTP restore coverage separately
	// verifies that the engine performs this transition for both token populations.
	mux.HandleFunc("/fixture/restore", func(w http.ResponseWriter, r *http.Request) {
		if _, err := pool.Exec(r.Context(), `UPDATE session_instance SET epoch=gen_random_uuid(); UPDATE sessions SET revoked_at=clock_timestamp()`); err != nil {
			http.Error(w, "fixture failed", 500)
			return
		}
		w.WriteHeader(204)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(runCtx, "node", "testdata/browser/session-reference.cjs", server.URL, ordinary, restored).CombinedOutput()
	t.Logf("reference: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='user.sessions.revoke'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pending intent replayed: %d %v", n, err)
	}
}
