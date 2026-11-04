package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestFieldLeaseIndicatorAuthorityFenceHTTP(t *testing.T) {
	for _, facet := range []string{"master-context", "service-entries"} {
		t.Run(facet, func(t *testing.T) {
			f := associationRepair(t)
			ctx := context.Background()
			c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Fence Customer").SaveX(ctx)
			a := f.asset(t, c.ID, "FENCE-ASSET")
			j := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Fence Job").SaveX(ctx)
			uid, _, _ := f.reader(t, []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}, {Permission: "service_entry.view", Scope: authorization.Assigned}})
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
			if facet == "service-entries" {
				path = fmt.Sprintf("/jobs/%s/assets/%d/service-entries", envelope.Data.Records[0].ID, a.ID)
			}
			if response = serviceAPIRequest(api, token, "GET", path, nil); response.Code != 200 {
				t.Fatalf("initial: %d", response.Code)
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
