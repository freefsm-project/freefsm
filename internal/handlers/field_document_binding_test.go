package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/jackc/pgx/v5/pgxpool"
)

func assignFieldDocumentCreator(t *testing.T, pool *pgxpool.Pool, job, user int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, job, user)
	if err != nil {
		t.Fatal(err)
	}
}

func TestFieldDocumentReviewedCustomerBindingAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reviewed Customer").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Different Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Reviewed Job").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, job.ID, admin); err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Binding review")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			operation, identity := newOperationKey(), newOperationKey()
			missing := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Reviewed creation","line_items":[]}`, operation, identity, job.ID)
			assertContains(t, lifecycleCall(t, api, token, "POST", "/"+kind+"s", missing, 428), "review_required")
			bound := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Reviewed creation","line_items":[]}`, operation, identity, job.ID, customer.ID)
			lifecycleCall(t, api, token, "POST", "/"+kind+"s", bound, 409)
			bound = fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Reviewed creation","line_items":[]}`, newOperationKey(), identity, job.ID, other.ID)
			assertContains(t, lifecycleCall(t, api, token, "POST", "/"+kind+"s", bound, 409), "commercial_conflict")
			// The reviewed identity remains the original Customer when the Job moves.
			bound = fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Before Customer drift","line_items":[]}`, newOperationKey(), identity, job.ID, customer.ID)
			client.Job.UpdateOneID(job.ID).SetCustomerID(other.ID).SaveX(ctx)
			assertContains(t, lifecycleCall(t, api, token, "POST", "/"+kind+"s", bound, 409), "commercial_conflict")
			client.Job.UpdateOneID(job.ID).SetCustomerID(customer.ID).SaveX(ctx)
			bound = fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Reviewed creation","line_items":[]}`, newOperationKey(), identity, job.ID, customer.ID)
			accepted := lifecycleCall(t, api, token, "POST", "/"+kind+"s", bound, 200)
			if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, admin); err != nil {
				t.Fatal(err)
			}
			if replay := lifecycleCall(t, api, token, "POST", "/"+kind+"s", bound, 200); replay != accepted {
				t.Fatalf("terminal replay changed: %s / %s", accepted, replay)
			}
			unassigned := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Broad grants are not assignment"}`, newOperationKey(), newOperationKey(), job.ID, customer.ID)
			lifecycleCall(t, api, token, "POST", "/"+kind+"s", unassigned, 403)
			if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, job.ID, admin); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFieldDocumentAssignmentRevocationAtCommitAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Commit Customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Commit Job").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, job.ID, admin)
			token, err := sessions.CreateMobile(ctx, admin, "Commit revocation")
			historyCheck(t, err)
			api := apiv1.NewRouter(pool, client, sessions)
			operation := newOperationKey()
			body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Reviewed before revocation","line_items":[]}`, operation, newOperationKey(), job.ID, customer.ID)
			fence, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer fence.Rollback(context.Background())
			var blocker int
			historyCheck(t, fence.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) x`).Scan(&blocker))
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", "/"+kind+"s", strings.NewReader(body)).WithContext(ctx)
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				done <- w
			}()
			commercialWaiters(t, ctx, pool, blocker, 1)
			_, err = fence.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, admin)
			historyCheck(t, err)
			historyCheck(t, fence.Commit(ctx))
			w := <-done
			if w.Code != 403 {
				t.Fatalf("revoked assignment = %d: %s", w.Code, w.Body.String())
			}
			assertContains(t, lifecycleCall(t, api, token, "GET", "/commercial-operations/"+operation, "", 200), `"outcome":"forbidden"`)
			var effects int
			historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM estimates)+(SELECT count(*) FROM invoices)+(SELECT count(*) FROM commercial_client_identities)+(SELECT count(*) FROM issued_document_numbers)+(SELECT count(*) FROM authorization_audit WHERE action IN ('estimate.create','invoice.create'))`).Scan(&effects))
			if effects != 0 {
				t.Fatalf("revoked creation left %d effects", effects)
			}
		})
	}
}
