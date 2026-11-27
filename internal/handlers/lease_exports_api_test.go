package handlers

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestLeaseExportAuthenticatedBearerAPIExclusion(t *testing.T) {
	pool, client, web, sessions, company, admin := leaseExportHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	populateFinalizedLease(t, pool, client, web, cookie, company)
	path := leaseExportPrepare(t, pool, web, cookie, url.Values{"mode": {"billing"}})
	token, err := sessions.CreateMobile(ctx, admin, "Lease export Administrator bearer")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewLeaseDirectory(objectref.NewEntDirectory(client), pool), t.TempDir(), 0))
	for _, target := range []string{"/leases/report", leaseExportReviewPath, "/exports", path, path + "/download"} {
		for _, method := range []string{"GET", "POST"} {
			r := httptest.NewRequest(method, target+"?mode=billing&billing=pending&archive=all", strings.NewReader(url.Values{"mode": {"billing"}, "operation_id": {newOperationKey()}, "action": {"download"}}.Encode()))
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatalf("mobile Administrator %s %s: %d %s", method, target, w.Code, w.Body.String())
			}
			for _, secret := range []string{"Finalized preservation Customer", "generated_total", "Covered equipment", "lease_number"} {
				assertNotContains(t, w.Body.String(), secret)
			}
		}
	}
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&count))
	historyEqual(t, "API probes do not accept exports", count, 1)
}
