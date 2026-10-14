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
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
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

func TestTransferPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser transfer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser transfer").SaveX(ctx)
	files := services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 25<<20)
	mux := chi.NewRouter()
	mux.Mount("/api/v1", apiv1.NewRouter(pool, client, sessions, files))
	mux.Handle("/static/*", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Mount("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "testdata/browser/pairing.cjs", server.URL)
	cmd.Env = append(os.Environ(), "PAIRING_BROWSER_COOKIE="+sessionCookie(t, ctx, sessions, admin).Value, fmt.Sprintf("TRANSFER_JOB=%d", j.ID))
	out, err := cmd.CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	if client.File.Query().CountX(ctx) != 0 {
		t.Fatal("reference harness published ordinary Files")
	}
	var transfers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM file_transfers WHERE transferred=size`).Scan(&transfers); err != nil || transfers != 3 {
		t.Fatalf("durable independent transfers=%d err=%v", transfers, err)
	}
}
