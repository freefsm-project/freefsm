package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

// The API accepts a reviewed Customer alongside the Job. An explicit mismatch
// must reject the whole edit, rather than silently ignoring part of the intent.
func TestCommercialEditReviewedCustomerAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reviewed edit Customer").SaveX(ctx)
			other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Different Customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Reviewed edit Job").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, job.ID, admin)
			api := apiv1.NewRouter(pool, client, sessions)
			token, err := sessions.CreateMobile(ctx, admin, "Reviewed commercial edit")
			if err != nil {
				t.Fatal(err)
			}
			created := lifecycleCall(t, api, token, "POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Original"}`, newOperationKey(), newOperationKey(), job.ID, customer.ID), 200)
			var receipt struct {
				Data struct {
					ID       int64  `json:"id"`
					Revision string `json:"revision"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(created), &receipt); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/%ss/%d", kind, receipt.Data.ID)
			before := lifecycleCall(t, api, token, "GET", path, "", 200)
			operation := newOperationKey()
			bad := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"customer_id":%d,"revision":%q,"title":"Must not save"}`, operation, job.ID, other.ID, receipt.Data.Revision)
			assertContains(t, lifecycleCall(t, api, token, "PATCH", path, bad, 409), `"commercial_conflict"`)
			assertContains(t, lifecycleCall(t, api, token, "GET", "/commercial-operations/"+operation, "", 200), `"outcome":"commercial_conflict"`)
			if after := lifecycleCall(t, api, token, "GET", path, "", 200); after != before {
				t.Fatalf("rejected edit changed current representation: before=%s after=%s", before, after)
			}
			var edits int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action=$1 AND target_id=$2`, kind+".edit", receipt.Data.ID).Scan(&edits); err != nil || edits != 0 {
				t.Fatalf("rejected edit audit count=%d err=%v", edits, err)
			}
			// Terminal repair is a newly reviewed operation, never changed same-ID intent.
			corrected := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"customer_id":%d,"revision":%q,"title":"Reviewed repair"}`, operation, job.ID, customer.ID, receipt.Data.Revision)
			assertContains(t, lifecycleCall(t, api, token, "PATCH", path, corrected, 409), `"operation_intent_changed"`)
			corrected = fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"customer_id":%d,"revision":%q,"title":"Reviewed repair"}`, newOperationKey(), job.ID, customer.ID, receipt.Data.Revision)
			accepted := lifecycleCall(t, api, token, "PATCH", path, corrected, 200)
			if replay := lifecycleCall(t, api, token, "PATCH", path, corrected, 200); replay != accepted {
				t.Fatalf("terminal replay changed: %s / %s", accepted, replay)
			}
			if err := json.Unmarshal([]byte(accepted), &receipt); err != nil {
				t.Fatal(err)
			}
			// Existing edit clients need not add Customer: their revision already
			// guards the anchor. Creation's explicit binding remains separate.
			legacy := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"notes":"Compatible edit"}`, newOperationKey(), job.ID, receipt.Data.Revision)
			lifecycleCall(t, api, token, "PATCH", path, legacy, 200)
			if replay := lifecycleCall(t, api, token, "PATCH", path, corrected, 200); replay != accepted {
				t.Fatalf("later commercial edits changed the original receipt: %s / %s", accepted, replay)
			}
			assertContains(t, lifecycleCall(t, api, token, "PATCH", path, bad, 409), `"commercial_conflict"`)
		})
	}
}
