package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/jackc/pgx/v5"
)

func TestScheduleRefreshPinsRenderedQueryBrowser(t *testing.T) {
	runScheduleRecoveryBrowser(t, "query")
}

func TestScheduleInitialReadFailureRecoveryBrowser(t *testing.T) {
	runScheduleRecoveryBrowser(t, "recovery")
}

// Real HTTP/session/authorization and migrated PostgreSQL; only fixture control
// endpoints sit outside the production router. Schema faults never run parallel.
func runScheduleRecoveryBrowser(t *testing.T, scenario string) {
	t.Helper()
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 to run persisted Chromium journeys")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Schedule recovery reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Recovery worker").SetEmail("recovery@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recovery customer").SaveX(ctx)
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	a := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Alpha recovery").SetStartTime(start).SetEndTime(start.Add(time.Hour)).SaveX(ctx)
	var finished int64
	if err = pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' LIMIT 1`, company).Scan(&finished); err != nil {
		t.Fatal(err)
	}
	b := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Beta recovery").SetStatusID(finished).SetStartTime(start.AddDate(0, 0, 1)).SetEndTime(start.AddDate(0, 0, 1).Add(time.Hour)).SaveX(ctx)
	for _, id := range []int64{a.ID, b.ID} {
		client.JobAssignment.Create().SetJobID(id).SetUserID(u.ID).SaveX(ctx)
	}
	var mu sync.Mutex
	broken := false
	setFault := func(fail bool) error {
		mu.Lock()
		defer mu.Unlock()
		if broken == fail {
			return nil
		}
		query := `ALTER TABLE jobs RENAME COLUMN end_time TO recovery_unavailable_end`
		if !fail {
			query = `ALTER TABLE jobs RENAME COLUMN recovery_unavailable_end TO end_time`
		}
		if _, e := pool.Exec(context.Background(), query); e != nil {
			return e
		}
		broken = fail
		t.Logf("actual database query fault active=%v", fail)
		return nil
	}
	defer func() {
		if e := setFault(false); e != nil {
			t.Errorf("restore column: %v", e)
		}
	}()
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.HandleFunc("/fixture/fault", func(w http.ResponseWriter, r *http.Request) {
		if e := setFault(r.URL.Query().Get("on") == "1"); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("/fixture/revoke", func(w http.ResponseWriter, r *http.Request) {
		e := auth.WithinActor(r.Context(), authorization.Actor{UserID: admin}, func(tx pgx.Tx, _ authorization.Principal) error {
			_, e := tx.Exec(r.Context(), `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, a.ID, u.ID)
			return e
		})
		if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	out := os.Getenv("SCHEDULE_REPAIR_EVIDENCE")
	if out == "" {
		out = t.TempDir()
	}
	out = filepath.Join(out, scenario)
	if err := os.MkdirAll(out, 0700); err != nil {
		t.Fatal(err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "node", "testdata/browser/schedule-recovery.cjs", server.URL, sessionCookie(t, ctx, sessions, u.ID).Value, sessionCookie(t, ctx, sessions, admin).Value, fmt.Sprint(a.ID), fmt.Sprint(b.ID), fmt.Sprint(u.ID), scenario, out)
	output, runErr := cmd.CombinedOutput()
	t.Logf("browser:\n%s", output)
	if e := os.WriteFile(filepath.Join(out, "browser.log"), output, 0600); e != nil {
		t.Error(e)
	}
	if e := setFault(false); e != nil {
		t.Fatal(e)
	}
	got := client.Job.GetX(ctx, a.ID)
	if got.StartTime == nil || got.EndTime == nil || !got.StartTime.Equal(start) || !got.EndTime.Equal(start.Add(time.Hour)) {
		t.Fatal("proposal changed persisted schedule before confirmation")
	}
	t.Log("schema restored; persisted Alpha interval unchanged: 2026-10-20 09:00–10:00 UTC")
	if runErr != nil {
		t.Fatal(runErr)
	}
}
