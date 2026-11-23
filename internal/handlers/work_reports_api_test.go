package handlers

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestWorkReportsBearerCannotAccessOfficeReportsOrExports(t *testing.T) {
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	token, err := sessions.CreateMobile(ctx, admin, "Office reports excluded")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	for _, path := range []string{"/reports/workload", "/reports/estimate-cohort", "/reports/workload/export", "/reports/estimate-cohort/export", "/jobs/export", "/estimates/export", "/invoices/export", "/exports/1/download"} {
		for _, method := range []string{"GET", "POST"} {
			t.Run(method+path, func(t *testing.T) {
				r := httptest.NewRequest(method, path, strings.NewReader("scope=A"))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				invalidJobID := method == "GET" && path == "/jobs/export" && w.Code == 400 && strings.Contains(w.Body.String(), `"code":"invalid_id"`)
				if w.Code != 404 && w.Code != 405 && !invalidJobID {
					t.Fatalf("field API exposed office surface: %d %s", w.Code, w.Body.String())
				}
				// The production office router must not upgrade a field bearer to
				// a browser session, even when its user is the Administrator.
				w = httptest.NewRecorder()
				r = httptest.NewRequest(method, path, strings.NewReader("scope=A"))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				web.ServeHTTP(w, r)
				if w.Code < 300 || (w.Code < 400 && !strings.Contains(w.Header().Get("Location"), "/login")) {
					t.Fatalf("office router accepted field bearer: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
	var accepted int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&accepted))
	if accepted != 0 {
		t.Fatal("bearer-only requests accepted export intent")
	}
}
