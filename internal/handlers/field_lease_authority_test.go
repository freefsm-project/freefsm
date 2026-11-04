package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestFieldLeaseIndicatorAuthorityFenceHTTP(t *testing.T) {
	fieldLeaseIndicatorAuthorityFenceHTTP(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}, {Permission: "service_entry.view", Scope: authorization.Assigned}}, false)
}

func TestFieldLeaseIndicatorBroadGrantAuthorityHTTP(t *testing.T) {
	fieldLeaseIndicatorAuthorityFenceHTTP(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "asset.view", Scope: authorization.All}, {Permission: "service_entry.view", Scope: authorization.All}}, true)
}

func fieldLeaseIndicatorAuthorityFenceHTTP(t *testing.T, grants []authorization.Grant, office bool) {
	t.Helper()
	for _, facet := range []string{"master-context", "numeric-master-context", "service-entries", "numeric-service-entries"} {
		t.Run(facet, func(t *testing.T) {
			f := associationRepair(t)
			ctx := context.Background()
			c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Fence Customer").SaveX(ctx)
			a := f.asset(t, c.ID, "FENCE-ASSET")
			j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Fence Job").SaveX(ctx)
			entriesPath := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
			serviceEntryPost(t, f.router, sessionCookie(t, ctx, f.sessions, f.admin), entriesPath, url.Values{"service_date": {time.Now().Format(time.DateOnly)}, "summary": {"Office history retained"}, "responsible_user_id": {fmt.Sprint(f.admin)}}, 303)
			uid, _, _ := f.reader(t, grants)
			f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
			token, err := f.sessions.CreateMobile(ctx, uid, "Fence reader")
			historyCheck(t, err)
			api := apiv1.NewRouter(f.pool, f.client, f.sessions)
			response := serviceAPIRequest(api, token, "GET", "/sync/jobs", nil)
			if response.Code != 200 {
				t.Fatalf("sync: %d %s", response.Code, response.Body.String())
			}
			var envelope struct {
				Data struct {
					Records []struct {
						ID string `json:"id"`
					} `json:"records"`
				} `json:"data"`
			}
			historyCheck(t, json.Unmarshal(response.Body.Bytes(), &envelope))
			if len(envelope.Data.Records) != 1 {
				t.Fatal("expected one assigned Job")
			}
			path := "/jobs/" + envelope.Data.Records[0].ID + "/master-context"
			if facet == "numeric-master-context" {
				path = fmt.Sprintf("/jobs/%d/master-context", j.ID)
			}
			if facet == "service-entries" {
				path = fmt.Sprintf("/jobs/%s/assets/%d/service-entries", envelope.Data.Records[0].ID, a.ID)
			}
			if facet == "numeric-service-entries" {
				path = fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
			}
			if response = serviceAPIRequest(api, token, "GET", path, nil); response.Code != 200 {
				t.Fatalf("initial: %d", response.Code)
			}
			_, err = f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, uid)
			historyCheck(t, err)
			response = serviceAPIRequest(api, token, "GET", path, nil)
			if response.Code != 404 {
				t.Fatalf("unassigned: %d %s", response.Code, response.Body.String())
			}
			if office {
				body := requestBody(t, f.router, sessionCookie(t, ctx, f.sessions, uid), "GET", entriesPath, 200)
				assertContains(t, body, "Office history retained")
				assertNotContains(t, body, "lease_coverage")
			}
			f.client.JobAssignment.Create().SetJobID(j.ID).SetUserID(uid).SaveX(ctx)
			if response = serviceAPIRequest(api, token, "GET", path, nil); response.Code != 200 {
				t.Fatalf("reassigned: %d %s", response.Code, response.Body.String())
			}
			// Hold the same production authority fence, wait for the real HTTP
			// reader, then commit assignment loss before it can observe data.
			tx, err := f.pool.Begin(ctx)
			historyCheck(t, err)
			defer tx.Rollback(ctx)
			_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`)
			historyCheck(t, err)
			var pid int
			historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid))
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- serviceAPIRequest(api, token, "GET", path, nil) }()
			commercialWaiters(t, ctx, f.pool, pid, 1)
			_, err = tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, uid)
			historyCheck(t, err)
			historyCheck(t, tx.Commit(ctx))
			response = <-done
			if response.Code != 404 {
				t.Fatalf("post-revocation read: %d %s", response.Code, response.Body.String())
			}
			assertNotContains(t, response.Body.String(), "lease_coverage")
		})
	}
}
