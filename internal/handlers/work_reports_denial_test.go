package handlers

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestWorkReportsDeniedAndInvalidSelectionHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	u := client.User.Create().SetCompanyID(company).SetName("No report grants").SetEmail("no-report-grants@test").SetRole("admin").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "No report grants", nil)
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	for _, path := range []string{"/reports/workload", "/reports/estimate-cohort"} {
		expectStatus(t, web, cookie, "GET", path+"?scope=A", 403)
		expectStatus(t, web, cookie, "GET", path+"/export?scope=A", 403)
		masterPost(t, web, cookie, path+"/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}, 403)
	}
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	for _, path := range []string{
		"/reports/workload?schedule_from=not-a-date",
		"/reports/workload?schedule_from=2026-12-01&schedule_to=2026-01-01",
		"/reports/workload?customer_id=-1",
		"/reports/workload?category=Invoice",
		"/reports/estimate-cohort?created_from=not-a-date",
		"/reports/estimate-cohort?created_from=2026-12-01&created_to=2026-01-01",
		"/reports/estimate-cohort?category=job%3Acompleted",
	} {
		expectStatus(t, web, adminCookie, "GET", path, 400)
	}
	var accepted int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&accepted))
	if accepted != 0 {
		t.Fatal("denied selection left accepted intent")
	}
}
