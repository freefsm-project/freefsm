package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5/pgxpool"
)

func commercialWaiters(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker, want int) {
	t.Helper()
	// Advisory locks are database-wide, but other packages can run fixtures in
	// this database concurrently. Their waiters cannot establish our race order.
	application := pool.Config().ConnConfig.RuntimeParams["application_name"]
	if application == "" {
		t.Fatal("commercial race requires a fixture-specific application name")
	}
	for {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, blocker, application).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture command did not reach the expected advisory fence")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestCommercialConcurrentNumberingAndReplayAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Concurrent commercial Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Concurrent commercial Job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Concurrent acceptance")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for _, kind := range []string{"estimate", "invoice"} {
		for _, replay := range []bool{false, true} {
			op, identity := newOperationKey(), newOperationKey()
			done := make(chan *httptest.ResponseRecorder, 6)
			for range 6 {
				key, clientID := op, identity
				if !replay {
					key, clientID = newOperationKey(), newOperationKey()
				}
				body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Concurrent","line_items":[]}`, key, clientID, j.ID, c.ID)
				go func() {
					r := httptest.NewRequest("POST", "/"+kind+"s", strings.NewReader(body))
					r.Header.Set("Authorization", "Bearer "+token)
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					api.ServeHTTP(w, r)
					done <- w
				}()
			}
			ids, numbers := map[int64]bool{}, map[string]bool{}
			for range 6 {
				w := <-done
				if w.Code != 200 {
					t.Fatalf("concurrent create=%d: %s", w.Code, w.Body.String())
				}
				var out struct {
					Data authorization.CommercialReceipt `json:"data"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				ids[out.Data.ID], numbers[out.Data.Number] = true, true
			}
			want := 6
			if replay {
				want = 1
			}
			if len(ids) != want || len(numbers) != want {
				t.Fatalf("identities=%v numbers=%v want %d", ids, numbers, want)
			}
		}
	}
}

func TestCommercialGrantLossAtCommitHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Revocation Customer").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Author").SetEmail("revoked-author@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "customer.view", Scope: authorization.All}, {Permission: "pricing.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Explicit author", append(append([]authorization.Grant{}, grants...), authorization.Grant{Permission: "estimate.create", Scope: authorization.All}))
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	fence, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(context.Background())
	var blocker int
	if err := fence.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) x`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	revoked := make(chan error, 1)
	go func() {
		_, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Renamed content reader", grants, 1)
		revoked <- err
	}()
	commercialWaiters(t, ctx, pool, blocker, 1)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- jobRepairRawPost(router, cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Denied at commit"}, "line_items": {"[]"}, "tax_rate": {"0"}})
	}()
	commercialWaiters(t, ctx, pool, blocker, 2)
	if err := fence.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != http.StatusForbidden {
		t.Fatalf("revoked create=%d: %s", w.Code, w.Body.String())
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM estimates`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denial left %d documents: %v", count, err)
	}
}

func TestCommercialJobReassignmentRejectsRacingDependencyHTTP(t *testing.T) {
	for _, dependency := range []string{"service_entry", "new_link", "finalize"} {
		t.Run(dependency, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race original").SaveX(ctx)
			next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race replacement").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Race Job").SaveX(ctx)
			if dependency == "finalize" {
				documentTransactionRequest(t, router, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Racing Invoice"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
			}
			path := fmt.Sprintf("/jobs/%d", j.ID)
			values := commercialReviewedValues(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(next.ID)}, "job_type": {"Race Job"}, "replace_customer": {"true"}})
			fence, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Rollback(context.Background())
			var blocker int
			if err := fence.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) x`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- jobRepairRawPost(router, cookie, path, values) }()
			commercialWaiters(t, ctx, pool, blocker, 1)
			switch dependency {
			case "service_entry":
				_, err = fence.Exec(ctx, `INSERT INTO job_customer_blockers(job_id,service_entry_id) VALUES($1,47001)`, j.ID)
			case "new_link":
				_, err = fence.Exec(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,title) VALUES($1,$2,$3,'Racing linked Estimate')`, company, c.ID, j.ID)
			case "finalize":
				_, err = fence.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`)
				if err == nil {
					_, err = fence.Exec(ctx, `UPDATE invoices SET status_id=(SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced' AND is_category_default) WHERE job_id=$2`, company, j.ID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := fence.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w := <-done
			if w.Code != 409 {
				t.Fatalf("racing %s gave %d: %s", dependency, w.Code, w.Body.String())
			}
			if got := client.Job.GetX(ctx, j.ID); got.CustomerID != c.ID {
				t.Fatal("racing dependency allowed partial Customer reassignment")
			}
		})
	}
}
