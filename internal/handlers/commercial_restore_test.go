package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestCommercialRestoreIndependentAuthorityHTTP(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained owner").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Historical Job").SaveX(ctx)
			var id int64
			if kind == "estimate" {
				id = client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Retained proposal").SetTerms("Captured terms").SetValidThrough(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)).SaveX(ctx).ID
			} else {
				var status int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void'`, company).Scan(&status))
				id = client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetStatusID(status).SetInvoiceDate(time.Now()).SetDueDate(time.Time{}).SetTitle("Retained Invoice").SetTerms("Captured terms").SaveX(ctx).ID
			}
			path := fmt.Sprintf("/%ss/%d", kind, id)
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
			client.Job.UpdateOneID(j.ID).SetDeletedAt(time.Now()).SaveX(ctx)
			svc := authorization.New(pool)
			grants := []authorization.Grant{{Permission: kind + ".view", Scope: authorization.All}, {Permission: kind + ".archived.view", Scope: authorization.All}, {Permission: kind + ".restore", Scope: authorization.All}}
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent restorer", grants)
			historyCheck(t, err)
			updateRole := func(next []authorization.Grant) {
				var revision int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
				_, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Renamed history custodian", next, revision)
				historyCheck(t, err)
			}
			updateRole(grants)
			u := client.User.Create().SetCompanyID(company).SetEmail("restorer@test").SetName("Restorer").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			assertContains(t, requestBody(t, web, cookie, "GET", path, 200), path+"/restore")
			var before, after string
			snapshot := `SELECT (to_jsonb(d)-'deleted_at'-'updated_at'-'commercial_revision')::text FROM ` + kind + `s d WHERE id=$1`
			var revision, restoredRevision int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT commercial_revision FROM `+kind+`s WHERE id=$1`, id).Scan(&revision))
			historyCheck(t, pool.QueryRow(ctx, snapshot, id).Scan(&before))
			fieldPost(t, web, cookie, path+"/restore", nil, 303)
			historyCheck(t, pool.QueryRow(ctx, snapshot, id).Scan(&after))
			historyCheck(t, pool.QueryRow(ctx, `SELECT commercial_revision FROM `+kind+`s WHERE id=$1`, id).Scan(&restoredRevision))
			if restoredRevision != revision+1 {
				t.Fatal("restore must invalidate pre-restore commercial intent")
			}
			if before != after || client.Job.GetX(ctx, j.ID).DeletedAt == nil {
				t.Fatalf("restore changed content/relations or reopened Job: before=%s after=%s", before, after)
			}
			fieldPost(t, web, cookie, path+"/delete", nil, 403)
			fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
			client.Customer.UpdateOneID(c.ID).SetDeletedAt(time.Now()).SaveX(ctx)
			assertNotContains(t, requestBody(t, web, cookie, "GET", path, 200), path+"/restore")
			fieldPost(t, web, cookie, path+"/restore", nil, 409)
			client.Customer.UpdateOneID(c.ID).ClearDeletedAt().SaveX(ctx)
			updateRole(grants[:2])
			assertNotContains(t, requestBody(t, web, cookie, "GET", path, 200), path+"/restore")
			fieldPost(t, web, cookie, path+"/restore", nil, 403)
			updateRole(grants[:1])
			fieldPost(t, web, cookie, path+"/restore", nil, 404)
			// Assigned Job visibility alone never confers either lifecycle grant.
			client.Job.UpdateOneID(j.ID).ClearDeletedAt().SaveX(ctx)
			_, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID)
			historyCheck(t, err)
			updateRole([]authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: kind + ".view", Scope: authorization.Assigned}})
			fieldPost(t, web, cookie, path+"/restore", nil, 404)
			fieldPost(t, web, adminCookie, path+"/restore", nil, 303)
			assertNotContains(t, requestBody(t, web, cookie, "GET", path, 200), path+"/delete")
			fieldPost(t, web, cookie, path+"/delete", nil, 403)
		})
	}
}

func TestCommercialLifecycleAuditAtomicHTTP(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic lifecycle").SaveX(ctx)
			var id int64
			if kind == "estimate" {
				id = client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Audit proposal").SaveX(ctx).ID
			} else {
				var status int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void'`, company).Scan(&status))
				id = client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetTitle("Audit Invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Time{}).SaveX(ctx).ID
			}
			cookie := sessionCookie(t, ctx, sessions, admin)
			path := fmt.Sprintf("/%ss/%d", kind, id)
			for _, action := range []string{"archive", "restore"} {
				_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION reject_commercial_lifecycle_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+kind+`.`+action+`' THEN RAISE EXCEPTION 'injected lifecycle audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_commercial_lifecycle_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_commercial_lifecycle_audit()`)
				historyCheck(t, err)
				var before, after string
				query := `SELECT to_jsonb(d)::text FROM ` + kind + `s d WHERE id=$1`
				historyCheck(t, pool.QueryRow(ctx, query, id).Scan(&before))
				suffix := "/restore"
				if action == "archive" {
					suffix = "/delete"
				}
				fieldPost(t, web, cookie, path+suffix, nil, 500)
				historyCheck(t, pool.QueryRow(ctx, query, id).Scan(&after))
				if before != after {
					t.Fatal("failed immutable audit changed document")
				}
				_, err = pool.Exec(ctx, `DROP TRIGGER reject_commercial_lifecycle_audit ON authorization_audit`)
				historyCheck(t, err)
				fieldPost(t, web, cookie, path+suffix, nil, 303)
				var recordedBefore, recordedAfter string
				historyCheck(t, pool.QueryRow(ctx, `SELECT (detail->'before')::text,(detail->'after')::text FROM authorization_audit WHERE action=$1 AND target_id=$2`, kind+"."+action, id).Scan(&recordedBefore, &recordedAfter))
				historyCheck(t, pool.QueryRow(ctx, query, id).Scan(&after))
				if recordedBefore != before || recordedAfter != after {
					t.Fatal("immutable lifecycle evidence does not match persisted before/after")
				}
			}
		})
	}
}

func TestCommercialRestoreRechecksWaitingAuthorityAndCustomerHTTP(t *testing.T) {
	for _, change := range []string{"customer", "grant"} {
		t.Run(change, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Waiting restore Customer").SaveX(ctx)
			e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Waiting restore").SaveX(ctx)
			path := fmt.Sprintf("/estimates/%d", e.ID)
			fieldPost(t, web, sessionCookie(t, ctx, sessions, admin), path+"/delete", nil, 303)
			svc := authorization.New(pool)
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Waiting restorer", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.archived.view", Scope: authorization.All}, {Permission: "estimate.restore", Scope: authorization.All}})
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetEmail("waiting-restorer@test").SetName("Waiting restorer").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			assertContains(t, requestBody(t, web, cookie, "GET", path, 200), path+"/restore")
			before := historyDurableRows(t, pool, []string{"estimates", "authorization_audit", "activity_logs"})
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var holder, waiter int
			historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
			done := make(chan *httptest.ResponseRecorder, 1)
			f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
			go func() { done <- f.call(creditCommand{path: path + "/restore"}) }()
			commercialWaiters(t, ctx, pool, holder, 1)
			historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&waiter))
			t.Logf("observed restore %s barrier holder=%d waiter=%d", change, holder, waiter)
			want := 409
			if change == "customer" {
				_, err = barrier.Exec(ctx, `UPDATE customers SET deleted_at=clock_timestamp() WHERE id=$1`, c.ID)
			} else {
				_, err = barrier.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='estimate.restore'`, role)
				want = 403
			}
			historyCheck(t, err)
			historyCheck(t, barrier.Commit(ctx))
			response := <-done
			if response.Code != want {
				t.Fatalf("waiting restore=%d want%d: %s", response.Code, want, response.Body.String())
			}
			historyEqual(t, "waiting denied Restore leaves document and history unchanged", historyDurableRows(t, pool, []string{"estimates", "authorization_audit", "activity_logs"}), before)
		})
	}
}
