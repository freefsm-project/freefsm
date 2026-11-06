package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestCommercialRestoreRequiresFreshFieldIntentAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Field archive Customer").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Field archive Job").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, j.ID, admin)
			api := apiv1.NewRouter(pool, client, sessions)
			token, err := sessions.CreateMobile(ctx, admin, "Archive field intent")
			historyCheck(t, err)
			created := lifecycleCall(t, api, token, "POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Field history","line_items":[]}`, newOperationKey(), newOperationKey(), j.ID, c.ID), 200)
			var result struct {
				Data struct {
					ID       int64  `json:"id"`
					Revision string `json:"revision"`
				} `json:"data"`
			}
			historyCheck(t, json.Unmarshal([]byte(created), &result))
			id := result.Data.ID
			path := fmt.Sprintf("/%ss/%d", kind, id)
			cookie := sessionCookie(t, ctx, sessions, admin)
			if kind == "invoice" {
				fieldPost(t, web, cookie, path+"/finalize", nil, 303)
			}
			read := func() string {
				var state struct {
					Data struct {
						Revision string `json:"revision"`
					} `json:"data"`
				}
				historyCheck(t, json.Unmarshal([]byte(lifecycleCall(t, api, token, "GET", path, "", 200)), &state))
				return state.Data.Revision
			}
			revision := read()
			op := newOperationKey()
			intent := func(op, revision string) string {
				return fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"notes":"Explicitly reviewed after restore"}`, op, j.ID, revision)
			}
			fieldPost(t, web, cookie, path+"/delete", nil, 303)
			fieldPost(t, web, cookie, path+"/restore", nil, 303)
			before := historyDurableRows(t, pool, []string{kind + "s", "authorization_audit", "commercial_operations"})
			lifecycleCall(t, api, token, "PATCH", path, intent(op, revision), 409)
			var notes string
			historyCheck(t, pool.QueryRow(ctx, `SELECT notes FROM `+kind+`s WHERE id=$1`, id).Scan(&notes))
			if notes != "" {
				t.Fatal("stale pending field action replayed")
			}
			// A stale operation may store a blocked receipt, but never document or audit effects.
			after := historyDurableRows(t, pool, []string{kind + "s", "authorization_audit", "commercial_operations"})
			historyEqual(t, "stale field intent preserves document", after[kind+"s"], before[kind+"s"])
			historyEqual(t, "stale field intent preserves audit", after["authorization_audit"], before["authorization_audit"])
			lifecycleCall(t, api, token, "PATCH", path, intent(newOperationKey(), read()), 200)
			historyCheck(t, pool.QueryRow(ctx, `SELECT notes FROM `+kind+`s WHERE id=$1`, id).Scan(&notes))
			if notes != "Explicitly reviewed after restore" {
				t.Fatal("fresh reviewed action did not persist")
			}
		})
	}
}
