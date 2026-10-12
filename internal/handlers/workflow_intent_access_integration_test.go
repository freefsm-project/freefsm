package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestWorkflowIntentUsesCurrentGrantsWithoutLegacyRoleFallback(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	administrator := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, administrator, 0, "Workflow field authority", nil)
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("workflow-grants@test").SetName("Workflow grants").SetRole("admin").SetPasswordHash("fixture").SaveX(ctx)
	if err = auth.AssignUser(ctx, administrator, user.ID, role); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Workflow customer").SaveX(ctx)
	var initial, done int64
	if err = pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:new' LIMIT 1`, company).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' LIMIT 1`, company).Scan(&done); err != nil {
		t.Fatal(err)
	}
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Scoped workflow").SetStatusID(initial).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, user.ID, "Workflow scoped client")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d/status", job.ID)
	call := func(method, body string, want int) string {
		t.Helper()
		url := path
		if method == "GET" {
			url += "-intents"
		}
		r := httptest.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s = %d want %d: %s", method, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	call("GET", "", 404) // Legacy admin grants no authority.
	revision := int64(1)
	grant := func(scope authorization.Scope, manage bool) {
		t.Helper()
		grants := []authorization.Grant{{Permission: "job.view", Scope: scope}}
		if manage {
			grants = append(grants, authorization.Grant{Permission: "job.status", Scope: scope})
		}
		if _, err = auth.SaveRole(ctx, administrator, role, "Renamed explicit workflow role", grants, revision); err != nil {
			t.Fatal(err)
		}
		revision++
	}
	grant(authorization.All, false)
	user = client.User.Create().SetCompanyID(company).SetEmail("workflow-tech@test").SetName("Equal grants tech").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	if err = auth.AssignUser(ctx, administrator, user.ID, role); err != nil {
		t.Fatal(err)
	}
	token, err = sessions.CreateMobile(ctx, user.ID, "Equal grants client")
	if err != nil {
		t.Fatal(err)
	}
	choices := call("GET", "", 200) // All Records works without assignment or office discriminator.
	var response struct {
		Data []struct {
			Intent statusflow.Intent `json:"intent"`
		} `json:"data"`
	}
	if err = json.Unmarshal([]byte(choices), &response); err != nil {
		t.Fatal(err)
	}
	var intent statusflow.Intent
	for _, choice := range response.Data {
		if choice.Intent.StatusID == done {
			intent = choice.Intent
		}
	}
	body, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	call("POST", string(body), 403) // View does not imply Status.
	grant(authorization.All, true)
	call("POST", string(body), 200)
	for _, target := range []int64{initial, done} {
		if err = json.Unmarshal([]byte(call("GET", "", 200)), &response); err != nil {
			t.Fatal(err)
		}
		for _, choice := range response.Data {
			if choice.Intent.StatusID == target {
				current, err := json.Marshal(choice.Intent)
				if err != nil {
					t.Fatal(err)
				}
				call("POST", string(current), 200)
			}
		}
	}
	// A completed active Job remains actionable for equal-grant legacy tech.
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	expectStatus(t, web, cookie, "POST", fmt.Sprintf("%s?status_id=%d", path, initial), 303)
	grant(authorization.Assigned, true)
	call("GET", "", 404)
	call("POST", string(body), 404)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(user.ID).SaveX(ctx)
	call("GET", "", 200)
	if _, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	call("GET", "", 404)
	grant(authorization.All, true)
	if _, err = pool.Exec(ctx, `UPDATE jobs SET deleted_at=now() WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	call("GET", "", 404)
}

func TestWorkflowIntentDiscoveryRechecksAuthorityAfterWaitingHTTP(t *testing.T) {
	for _, change := range []string{"grant", "assignment"} {
		t.Run(change, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			auth := authorization.New(pool)
			a := authorization.Actor{UserID: admin}
			role, err := auth.SaveRole(ctx, a, 0, "Assigned discovery", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}})
			if err != nil {
				t.Fatal(err)
			}
			user := client.User.Create().SetCompanyID(company).SetEmail("waiting-intent@test").SetName("Waiting reader").SetPasswordHash("fixture").SetRole("admin").SaveX(ctx)
			if err = auth.AssignUser(ctx, a, user.ID, role); err != nil {
				t.Fatal(err)
			}
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Waiting customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Waiting job").SaveX(ctx)
			client.JobAssignment.Create().SetJobID(job.ID).SetUserID(user.ID).SaveX(ctx)
			token, err := sessions.CreateMobile(ctx, user.ID, "Waiting discovery")
			if err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			var blocker int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			api := apiv1.NewRouter(pool, client, sessions)
			go func() {
				r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/status-intents", job.ID), nil).WithContext(ctx)
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				done <- w
			}()
			for {
				var waiting bool
				if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case w := <-done:
					t.Fatalf("discovery bypassed authority fence: %d %s", w.Code, w.Body.String())
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if change == "grant" {
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1`, role)
			} else {
				_, err = tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, user.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				if w.Code != 404 || strings.Contains(w.Body.String(), "meaning_revision") {
					t.Fatalf("stale authority disclosed choices: %d %s", w.Code, w.Body.String())
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestWorkflowIntentPresentationAndMeaningAreOneSnapshotHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Snapshot customer").SaveX(ctx)
	var initial int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:new' LIMIT 1`, company).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Snapshot job").SetStatusID(initial).SaveX(ctx)
	svc := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	target, err := svc.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Job, Name: "Waiting", Category: statusflow.JobPending})
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.ObserveIntent(ctx, company, statusflow.Job, job.ID, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	var moveErr error
	go func() {
		defer close(done)
		<-start
		moveErr = svc.Move(ctx, actor, statusflow.MoveRequest{StatusID: target.ID, Category: statusflow.JobInProgress})
	}()
	// Schedule an actual concurrent definition change immediately after the old
	// Ent presentation read. The replacement single-query boundary does not use
	// this split read; in that case the writer runs just after the response.
	client.Status.Intercept(ent.InterceptFunc(func(next ent.Querier) ent.Querier {
		return ent.QuerierFunc(func(ctx context.Context, q ent.Query) (ent.Value, error) {
			v, err := next.Query(ctx, q)
			once.Do(func() { close(start) })
			<-done
			return v, err
		})
	}))
	token, err := sessions.CreateMobile(ctx, admin, "Snapshot client")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/status-intents", job.ID), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	apiv1.NewRouter(pool, client, sessions).ServeHTTP(w, r)
	once.Do(func() { close(start) })
	<-done
	if moveErr != nil {
		t.Fatal(moveErr)
	}
	if w.Code != 200 {
		t.Fatalf("discovery: %d %s", w.Code, w.Body.String())
	}
	var envelope struct {
		Data []struct {
			Status struct {
				ID       int64                  `json:"id"`
				Category statusflow.CategoryKey `json:"category"`
			} `json:"status"`
			Intent statusflow.Intent `json:"intent"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	after, err := svc.ObserveIntent(ctx, company, statusflow.Job, job.ID, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, choice := range envelope.Data {
		if choice.Status.ID != target.ID {
			continue
		}
		found = true
		if choice.Status.Category == statusflow.JobPending && choice.Intent.MeaningRevision == before.MeaningRevision {
			continue
		}
		if choice.Status.Category == statusflow.JobInProgress && choice.Intent.MeaningRevision == after.MeaningRevision {
			continue
		}
		t.Fatalf("presentation/token from different meanings: %+v (before=%s after=%s)", choice, before.MeaningRevision, after.MeaningRevision)
	}
	if !found {
		t.Fatal("missing selectable target")
	}
}
