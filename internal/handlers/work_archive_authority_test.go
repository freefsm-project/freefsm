package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestWorkArchiveRestoreRechecksWaitingAuthorityHTTP(t *testing.T) {
	workLifecycleWaitingAuthority(t, "restore")
}

func TestWorkArchiveRechecksWaitingAuthorityHTTP(t *testing.T) {
	workLifecycleWaitingAuthority(t, "archive")
}

func workLifecycleWaitingAuthority(t *testing.T, action string) {
	changes := []string{"grant", "session"}
	suffix := "/delete"
	if action == "restore" {
		changes = append(changes, "customer")
		suffix = "/restore"
	}
	for _, kind := range []string{"job", "project"} {
		for _, change := range changes {
			t.Run(kind+"/"+change, func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Waiting restore owner").SaveX(ctx)
				var status, id int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2 AND is_category_default`, company, kind+":completed").Scan(&status))
				if kind == "job" {
					id = client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetJobType("Waiting work").SaveX(ctx).ID
				} else {
					id = client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetName("Waiting work").SaveX(ctx).ID
				}
				path := fmt.Sprintf("/%ss/%d", kind, id)
				fieldPost(t, web, sessionCookie(t, ctx, sessions, admin), path+"/delete", nil, 303)
				if action == "archive" {
					fieldPost(t, web, sessionCookie(t, ctx, sessions, admin), path+"/restore", nil, 303)
				}
				auth := authorization.New(pool)
				role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Waiting custodian", []authorization.Grant{{Permission: kind + ".view", Scope: authorization.All}, {Permission: kind + ".archived.view", Scope: authorization.All}, {Permission: kind + "." + action, Scope: authorization.All}})
				historyCheck(t, err)
				u := client.User.Create().SetCompanyID(company).SetName("Custodian").SetEmail("waiting-work@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
				historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
				cookie := sessionCookie(t, ctx, sessions, u.ID)
				assertContains(t, requestBody(t, web, cookie, "GET", path, 200), path+suffix)
				tables := []string{kind + "s", "authorization_audit", "activity_logs"}
				before := historyDurableRows(t, pool, tables)
				barrier, err := pool.Begin(ctx)
				historyCheck(t, err)
				defer barrier.Rollback(context.Background())
				var holder, waiter int
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
				f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- f.call(creditCommand{path: path + suffix}) }()
				commercialWaiters(t, ctx, pool, holder, 1)
				historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&waiter))
				t.Logf("observed %s %s/%s holder=%d waiter=%d", kind, action, change, holder, waiter)
				want := 403
				switch change {
				case "grant":
					_, err = barrier.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, kind+"."+action)
				case "session":
					_, err = barrier.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, u.ID)
				case "customer":
					_, err = barrier.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, c.ID)
					want = 409
				}
				historyCheck(t, err)
				historyCheck(t, barrier.Commit(ctx))
				response := <-done
				if response.Code != want {
					t.Fatalf("%s=%d want%d: %s", action, response.Code, want, response.Body.String())
				}
				historyEqual(t, "current authority denial rolls back whole "+action, historyDurableRows(t, pool, tables), before)
				if change == "grant" {
					assertNotContains(t, requestBody(t, web, cookie, "GET", path, 200), path+suffix)
				}
			})
		}
	}
}
