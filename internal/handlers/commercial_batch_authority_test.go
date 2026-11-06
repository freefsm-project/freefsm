package handlers

import (
	"context"
	"fmt"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestCommercialBatchCurrentAuthorityAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Authority customer").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Authority job").SaveX(ctx)
			u := client.User.Create().SetCompanyID(company).SetName("Batch editor").SetEmail("batch-editor@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
			auth := authorization.New(pool)
			actor := authorization.Actor{UserID: admin}
			grants := []authorization.Grant{{Permission: kind + ".view", Scope: authorization.Assigned}, {Permission: kind + ".edit", Scope: authorization.Assigned}}
			role, err := auth.SaveRole(ctx, actor, 0, "Batch independent edit", grants)
			historyCheck(t, err)
			historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
			assignFieldDocumentCreator(t, pool, j.ID, admin)
			assignFieldDocumentCreator(t, pool, j.ID, u.ID)
			adminToken, err := sessions.CreateMobile(ctx, admin, "Batch creator")
			historyCheck(t, err)
			token, err := sessions.CreateMobile(ctx, u.ID, "Batch editor")
			historyCheck(t, err)
			f := batchCommercialFixture{pool: pool, api: apiv1.NewRouter(pool, client, sessions), token: adminToken, kind: kind, job: j.ID, customer: c.ID}
			state := f.create(t)
			f.token = token
			op := f.operation(state, "edit", map[string]any{"notes": "Authorized edit"})
			batchOutcome(t, batchResults(t, f, op)[0], "accepted")
			current := matrixState(t, f.read(t, state.ID))
			for _, choice := range current.Workflow {
				category, action := "estimate:accepted", "status"
				if kind == "invoice" {
					category, action = "invoice:invoiced", "finalize"
				}
				if choice.Category == category {
					workflow := f.operation(current, action, map[string]any{"status_id": choice.ID, "meaning_revision": choice.Meaning})
					batchOutcome(t, batchResults(t, f, workflow)[0], "forbidden")
				}
			}
			_, err = auth.SaveRole(ctx, actor, role, "Batch no authority", []authorization.Grant{}, 1)
			historyCheck(t, err)
			// Original receipts remain nonsensitive and immutable after grant loss.
			batchOutcome(t, batchResults(t, f, op)[0], "accepted")
			lifecycleCall(t, f.api, token, "GET", "/commercial-operations/"+op["operation_id"].(string), "", 200)
			fresh := f.operation(current, "edit", map[string]any{"notes": "Forbidden edit"}, op["operation_id"].(string))
			result := batchResults(t, f, fresh)[0]
			if result.Receipt["outcome"] != "forbidden" && result.Receipt["outcome"] != "not_found" {
				t.Fatalf("current authority bypassed: %+v", result)
			}
			var notes string
			historyCheck(t, pool.QueryRow(ctx, fmt.Sprintf("SELECT notes FROM %ss WHERE id=$1", kind), state.ID).Scan(&notes))
			if notes != "Authorized edit" {
				t.Fatal("denied batch changed content")
			}
		})
	}
}
