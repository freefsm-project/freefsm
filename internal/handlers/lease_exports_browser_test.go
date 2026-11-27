package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestLeaseExportPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	svc, actor := authorization.New(pool), authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser cohort Customer").SaveX(ctx)
	var expected []string
	for i := 0; i < 103; i++ {
		lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
		historyCheck(t, err)
		historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: fmt.Sprintf("Browser cohort %03d, 雪 \"quoted\"\nsecond line", i)}))
		expected = append(expected, fmt.Sprint(lease.ID))
	}
	reader, _ := leaseExportReader(t, pool, client, company, admin, "Browser report only", leaseExportGrants("lease.view"))
	base, _ := leaseExportReader(t, pool, client, company, admin, "Browser base exporter", leaseExportGrants("lease.view", "lease.export"))
	denied, deniedRole := leaseExportReader(t, pool, client, company, admin, "Browser lost selection", leaseExportGrants("lease.view", "lease.export", "customer.view"))
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	expired := leaseExportPrepare(t, pool, web, adminCookie, url.Values{"mode": {"billing"}, "search": {"Browser cohort"}, "sort": {"number_desc"}})
	deniedPath := leaseExportPrepare(t, pool, web, sessionCookie(t, ctx, sessions, denied), url.Values{"mode": {"operational"}, "customer": {fmt.Sprint(c.ID)}, "search": {"PRIVATE RECOVERY FILTER"}})
	_, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '24 hours' WHERE id=$1 OR id=$2`, strings.TrimPrefix(expired, "/exports/"), strings.TrimPrefix(deniedPath, "/exports/"))
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='customer.view'`, deniedRole)
	historyCheck(t, err)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(web)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	outputDir := t.TempDir()
	config, err := json.Marshal(map[string]string{"base": server.URL, "admin": adminCookie.Value, "reader": sessionCookie(t, ctx, sessions, reader).Value, "operational": sessionCookie(t, ctx, sessions, base).Value, "denied": sessionCookie(t, ctx, sessions, denied).Value, "expired": expired, "deniedPath": deniedPath, "output": outputDir})
	historyCheck(t, err)
	cmd := exec.Command("node", "testdata/browser/lease-exports.cjs", string(config))
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	historyCheck(t, err)
	// Parse real Chromium download files with Go's independent standard CSV
	// reader. A successful click or substring assertion is not complete evidence.
	for _, mode := range []string{"operational", "billing", "regenerated"} {
		bytes, err := os.ReadFile(filepath.Join(outputDir, mode+".csv"))
		historyCheck(t, err)
		rows := leaseExportRows(t, string(bytes))
		leases := leaseExportKind(rows, "lease")
		historyEqual(t, "browser downloads all pages", len(leases), 103)
		want := slices.Clone(expected)
		slices.Reverse(want)
		for i, row := range leases {
			historyEqual(t, "downloaded stable Number order", row["lease_id"], want[i])
			assertContains(t, row["title"], "雪 \"quoted\"\nsecond line")
		}
		summary := leaseExportKind(rows, "summary")[0]
		historyEqual(t, "downloaded distinct summary", summary["matching_lease_count"], "103")
		if mode != "operational" {
			historyEqual(t, "Draft rent total", summary["matching_monthly_rent"], "0.00")
		}
	}
}
