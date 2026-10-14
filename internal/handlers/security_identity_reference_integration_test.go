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
	"github.com/go-chi/chi/v5"
)

func TestSecurityIdentityReferenceLostResponsesHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "identity reference")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	output, err := exec.Command("node", "../api/v1/testdata/security-identity-reference.mjs", server.URL, token).CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
	var leases, receipts, audits int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM time_clock_leases),(SELECT count(*) FROM clock_lease_receipts),(SELECT count(*) FROM authorization_audit WHERE action='time.clock_lease.issue')`).Scan(&leases, &receipts, &audits); err != nil {
		t.Fatal(err)
	}
	if leases != 2 || receipts != 2 || audits != 2 {
		t.Fatalf("two distinct issuance intents: leases=%d receipts=%d audits=%d", leases, receipts, audits)
	}
}

func TestSecurityIdentityPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := sessions.CreateMobile(ctx, admin, "Security target"); err != nil {
		t.Fatal(err)
	}
	var target int64
	if err := pool.QueryRow(ctx, `SELECT id FROM sessions WHERE device_name='Security target'`).Scan(&target); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	mux.Mount("/api/v1", apiv1.NewRouter(pool, client, sessions))
	mux.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Mount("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "testdata/browser/security-identity.cjs", server.URL)
	cmd.Env = append(os.Environ(), "SECURITY_BROWSER_COOKIE="+sessionCookie(t, ctx, sessions, admin).Value, fmt.Sprintf("SECURITY_TARGET_SESSION=%d", target))
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
	var receipts, audits int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_revocation_receipts),(SELECT count(*) FROM authorization_audit WHERE action='user.sessions.revoke')`).Scan(&receipts, &audits); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || audits != 1 {
		t.Fatalf("browser duplicated receipts=%d audits=%d", receipts, audits)
	}
}

func TestSecurityIdentityFreshAuthenticationReadOnlyBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	for _, accountSwitch := range []bool{false, true} {
		t.Run(fmt.Sprintf("account_switch_%t", accountSwitch), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			freshUser := admin
			if accountSwitch {
				freshUser = client.User.Create().SetCompanyID(company).SetEmail("fresh-account@test").SetName("Fresh account").SetRole("dispatcher").SetPasswordHash("hash").SaveX(ctx).ID
			}
			mux := chi.NewRouter()
			mux.Mount("/api/v1", apiv1.NewRouter(pool, client, sessions))
			mux.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
			mux.Mount("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
			server := httptest.NewServer(mux)
			defer server.Close()
			runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			cmd := exec.CommandContext(runCtx, "node", "testdata/browser/security-fresh-auth.cjs", server.URL)
			cmd.Env = append(os.Environ(), "SECURITY_BROWSER_COOKIE="+sessionCookie(t, ctx, sessions, admin).Value, "SECURITY_FRESH_COOKIE="+sessionCookie(t, ctx, sessions, freshUser).Value)
			output, err := cmd.CombinedOutput()
			t.Log(string(output))
			if err != nil {
				t.Fatal(err)
			}
			var receipts int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_revocation_receipts`).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if receipts != 1 {
				t.Fatalf("fresh client replayed old unreceived action: receipts=%d", receipts)
			}
		})
	}
}
