package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestActivityExportPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser activity export").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,object_type,object_id,action,created_at) SELECT $1,$2,'customer',$3,'updated','2026-02-15'::timestamptz FROM generate_series(1,35)`, company, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	browserCtx, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/activity-export.cjs", server.URL, cookie.Value).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
