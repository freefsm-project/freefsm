package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestCommercialServerEditCommitOrdersAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		for _, change := range []string{"document archive", "job archive", "item archive", "assignment", "pricebook grant", "pricing grant", "edit grant"} {
			for _, changeFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/changeFirst=%v", kind, change, changeFirst), func(t *testing.T) {
					pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Edit race owner").SaveX(ctx)
					var finished int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&finished))
					j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(finished).SetJobType("Edit race").SaveX(ctx)
					item := client.Item.Create().SetCompanyID(company).SetName("Selected input").SetType("service").SetUnitPrice(10).SaveX(ctx)
					u := client.User.Create().SetCompanyID(company).SetName("Editor").SetEmail("edit-race@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
					grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: kind + ".view", Scope: authorization.Assigned}, {Permission: kind + ".edit", Scope: authorization.Assigned}, {Permission: "pricing.view", Scope: authorization.Assigned}, {Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All}}
					auth := authorization.New(pool)
					actor := authorization.Actor{UserID: admin}
					role, err := auth.SaveRole(ctx, actor, 0, "Editor race", grants)
					historyCheck(t, err)
					historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
					assignFieldDocumentCreator(t, pool, j.ID, u.ID)
					assignFieldDocumentCreator(t, pool, j.ID, admin)
					token, err := sessions.CreateMobile(ctx, u.ID, "Edit race")
					historyCheck(t, err)
					adminToken, err := sessions.CreateMobile(ctx, admin, "Create race document")
					historyCheck(t, err)
					api := apiv1.NewRouter(pool, client, sessions)
					created := matrixState(t, lifecycleCall(t, api, adminToken, "POST", "/"+kind+"s", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "customer_id": c.ID, "title": "Before race"}), 200))
					path := fmt.Sprintf("/%ss/%d", kind, created.ID)
					readAdmin := func() commercialMatrixState {
						return matrixState(t, lifecycleCall(t, api, adminToken, "GET", path, "", 200))
					}
					// Both kinds are legitimately archivable; no fixture bypass of archive rules.
					if kind == "invoice" {
						matrixTransition(t, api, adminToken, path, j.ID, readAdmin(), "invoice:invoiced", "finalize", 200)
						matrixTransition(t, api, adminToken, path, j.ID, readAdmin(), "invoice:void", "status", 200)
					} else {
						matrixTransition(t, api, adminToken, path, j.ID, readAdmin(), "estimate:rejected", "status", 200)
					}
					state := matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200))
					cookie := sessionCookie(t, ctx, sessions, admin)
					var mutate func() int
					switch change {
					case "document archive":
						mutate = func() int { return jobRepairRawPost(web, cookie, path+"/delete", nil).Code }
					case "job archive":
						mutate = func() int { return jobRepairRawPost(web, cookie, fmt.Sprintf("/jobs/%d/delete", j.ID), nil).Code }
					case "item archive":
						mutate = func() int { return jobRepairRawPost(web, cookie, fmt.Sprintf("/items/%d/delete", item.ID), nil).Code }
					case "assignment":
						review, err := auth.ReviewDispatch(ctx, actor, j.ID, authorization.DispatchPlan{})
						historyCheck(t, err)
						mutate = func() int {
							if err := auth.ConfirmDispatch(ctx, actor, j.ID, review.ID, true); err != nil {
								return 409
							}
							return 303
						}
					default:
						permission := map[string]string{"pricebook grant": "pricebook.view", "pricing grant": "pricing.view", "edit grant": kind + ".edit"}[change]
						next := []authorization.Grant{}
						for _, g := range grants {
							if g.Permission != permission {
								next = append(next, g)
							}
						}
						mutate = func() int {
							if _, err := auth.SaveRole(ctx, actor, role, "Editor race", next, 1); err != nil {
								return 409
							}
							return 303
						}
					}
					op := newOperationKey()
					body := json.RawMessage(matrixBody(t, map[string]any{"operation_id": op, "job_id": j.ID, "customer_id": c.ID, "revision": state.Revision, "notes": "Accepted edit", "line_items": []map[string]any{{"item_id": item.ID, "title": "Reviewed snapshot", "quantity": "1", "unit_price": "9.00", "taxable": false}}}))
					client.Item.UpdateOneID(item.ID).SetUnitPrice(42).SaveX(ctx)
					barrier, err := pool.Begin(ctx)
					historyCheck(t, err)
					defer barrier.Rollback(context.Background())
					var holder, first int
					historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
					edited := make(chan *httptest.ResponseRecorder, 1)
					changed := make(chan int, 1)
					runEdit := func() { edited <- serviceAPIRequest(api, token, "PATCH", path, body) }
					runChange := func() { changed <- mutate() }
					if changeFirst {
						go runChange()
					} else {
						go runEdit()
					}
					commercialWaiters(t, ctx, pool, holder, 1)
					historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&first))
					if changeFirst {
						go runEdit()
					} else {
						go runChange()
					}
					commercialWaiters(t, ctx, pool, first, 1)
					historyCheck(t, barrier.Commit(ctx))
					w, status := <-edited, <-changed
					if status != 303 {
						t.Fatalf("change=%d edit=%d %s", status, w.Code, w.Body.String())
					}
					if changeFirst {
						if w.Code != 403 && w.Code != 404 && w.Code != 409 {
							t.Fatalf("lost eligibility edit=%d %s", w.Code, w.Body.String())
						}
					} else if w.Code != 200 {
						t.Fatalf("winning edit=%d %s", w.Code, w.Body.String())
					}
					var notes, total string
					var customer, job int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT notes,customer_id,job_id,financial::jsonb->'result'->>'total_cents' FROM `+kind+`s WHERE id=$1`, created.ID).Scan(&notes, &customer, &job, &total))
					wantNotes, wantTotal := "Accepted edit", "900"
					if changeFirst {
						wantNotes, wantTotal = "", "0"
					}
					if notes != wantNotes || total != wantTotal || customer != c.ID || job != j.ID {
						t.Fatalf("partial/repriced/retargeted edit: %q %s %d %d", notes, total, customer, job)
					}
					if !changeFirst {
						if replay := serviceAPIRequest(api, token, "PATCH", path, body); replay.Code != 200 || replay.Body.String() != w.Body.String() {
							t.Fatalf("scope-loss replay changed: %d %s", replay.Code, replay.Body.String())
						}
						if receipt := lifecycleCall(t, api, token, "GET", "/commercial-operations/"+op, "", 200); receipt != w.Body.String() {
							t.Fatal("scope-loss lookup changed receipt")
						}
						var changedIntent map[string]any
						historyCheck(t, json.Unmarshal(body, &changedIntent))
						changedIntent["notes"] = "Changed same-ID intent"
						assertContains(t, lifecycleCall(t, api, token, "PATCH", path, matrixBody(t, changedIntent), 409), `"operation_intent_changed"`)
					}
					var audits int
					historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action=$1 AND target_id=$2`, kind+".edit", created.ID).Scan(&audits))
					wantAudits := 1
					if changeFirst {
						wantAudits = 0
					}
					if audits != wantAudits {
						t.Fatalf("edit audits=%d want%d", audits, wantAudits)
					}
				})
			}
		}
	}
}
