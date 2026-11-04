package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestLeaseAdjustmentBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var invoice, asset int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice))
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-adjustment.cjs", server.URL, cookie.Value, fmt.Sprint(lease), fmt.Sprint(asset), fmt.Sprint(invoice)).CombinedOutput()
	t.Logf("adjustment browser: %s", output)
	historyCheck(t, err)
	var n int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_manual_adjustments WHERE lease_id=$1`, lease).Scan(&n))
	historyEqual(t, "two explicit persisted browser applications", n, 2)
}
