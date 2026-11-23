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
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
)

func workReportRecoveryReceipts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, web http.Handler, cookie *http.Cookie, route string, filters url.Values) (string, string) {
	t.Helper()
	filters.Set("operation_id", newOperationKey())
	expired := masterPost(t, web, cookie, route, filters, 303)
	_, err := exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '25 hours' WHERE id=$1`, strings.TrimPrefix(expired, "/exports/"))
	historyCheck(t, err)
	filters.Set("operation_id", newOperationKey())
	failed := masterPost(t, web, cookie, route, filters, 303)
	_, err = pool.Exec(ctx, `CREATE FUNCTION recovery_storage_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private recovery fixture storage failure' USING ERRCODE='58030'; END $$; CREATE TRIGGER recovery_storage_fault BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION recovery_storage_fault()`)
	historyCheck(t, err)
	if _, err = exports.Product(pool).ProcessOne(ctx); err == nil {
		t.Fatal("recovery fixture generation unexpectedly succeeded")
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER recovery_storage_fault ON export_records; DROP FUNCTION recovery_storage_fault()`)
	historyCheck(t, err)
	assertContains(t, requestBody(t, web, cookie, "GET", failed, 200), "Failed")
	filters.Del("operation_id")
	return expired, failed
}

func TestWorkReportsRecoveryPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private recovery Customer").SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Private recovery fact").SaveX(ctx)
	client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Private recovery fact").SaveX(ctx)
	client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Private recovery fact").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	type journey struct {
		Route, Expired, Failed string
		Filters                url.Values
	}
	var journeys []journey
	for _, route := range []string{"/reports/workload/export", "/reports/estimate-cohort/export", "/jobs/export", "/estimates/export", "/invoices/export"} {
		filters := url.Values{"scope": {"A"}, "customer_id": {fmt.Sprint(c.ID)}}
		switch route {
		case "/reports/workload/export":
			filters.Set("measure", "unscheduled")
			filters.Set("schedule_from", "2020-01-01")
			filters.Set("schedule_to", "2030-01-01")
		case "/reports/estimate-cohort/export":
			filters.Set("conversion", "none")
			filters.Set("created_from", "2020-01-01")
			filters.Set("created_to", "2030-01-01")
			filters.Set("sort", "created_asc")
		default:
			filters.Set("search", "Private recovery")
			if route == "/jobs/export" {
				filters.Set("sort", "schedule_desc")
			} else {
				filters.Set("sort", "title_asc")
			}
		}
		expired, failed := workReportRecoveryReceipts(t, ctx, pool, web, cookie, route, filters)
		journeys = append(journeys, journey{route, expired, failed, filters})
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(web)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	done := make(chan struct{})
	go func() { defer close(done); _ = exports.Product(pool).Run(ctx, nil, nil) }()
	defer func() { cancel(); <-done }()
	raw, err := json.Marshal(journeys)
	historyCheck(t, err)
	cmd := exec.CommandContext(ctx, "node", "testdata/browser/work-report-recovery.cjs", server.URL, cookie.Value, string(raw))
	output, err := cmd.CombinedOutput()
	t.Log(string(output))
	historyCheck(t, err)
}

func TestWorkReportsRecoveryHidesSelectionAfterGrantLossHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	u := client.User.Create().SetCompanyID(company).SetName("Recovery requester").SetEmail("recovery-requester@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, actor, 0, "Recovery requester", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.export", Scope: authorization.All}})
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private recovery Customer").SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("PRIVATE retained filter").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	expired, failed := workReportRecoveryReceipts(t, ctx, pool, web, cookie, "/jobs/export", url.Values{"scope": {"A"}, "search": {"PRIVATE retained filter"}})
	for _, path := range []string{expired, failed} {
		assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "PRIVATE")
	}
	var revision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
	_, err = auth.SaveRole(ctx, actor, role, "Recovery requester", nil, revision)
	historyCheck(t, err)
	for _, path := range []string{expired, failed} {
		body := requestBody(t, web, cookie, "GET", path, 200)
		assertNotContains(t, body, "PRIVATE")
		assertNotContains(t, body, "/jobs/export?")
	}
}

func TestWorkReportsRecoveryHidesExpiredSelectionAfterRowLossHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	u := client.User.Create().SetCompanyID(company).SetName("Assigned recovery requester").SetEmail("assigned-recovery@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, actor, 0, "Assigned recovery", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.export", Scope: authorization.Assigned}})
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recovery row owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("PRIVATE lost row").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	expired, _ := workReportRecoveryReceipts(t, ctx, pool, web, cookie, "/jobs/export", url.Values{"scope": {"J"}, "search": {"PRIVATE lost row"}})
	assertContains(t, requestBody(t, web, cookie, "GET", expired, 200), "PRIVATE")
	_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID)
	historyCheck(t, err)
	body := requestBody(t, web, cookie, "GET", expired, 200)
	assertNotContains(t, body, "PRIVATE")
	assertNotContains(t, body, "/jobs/export?")
}
