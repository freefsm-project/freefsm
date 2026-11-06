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

func TestFieldDocumentCreationCommitOrdersHTTP(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		for _, change := range []string{"job archive", "customer archive", "item archive", "assignment", "pricebook grant", "pricing grant", "customer drift"} {
			for _, changeFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%s/changeFirst=%v", kind, change, changeFirst), func(t *testing.T) {
					pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race owner").SaveX(ctx)
					other := client.Customer.Create().SetCompanyID(company).SetDisplayName("New owner").SaveX(ctx)
					var finished int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&finished))
					j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(finished).SetJobType("Race work").SaveX(ctx)
					item := client.Item.Create().SetCompanyID(company).SetName("Selected snapshot").SetType("service").SetUnitPrice(10).SaveX(ctx)
					u := client.User.Create().SetCompanyID(company).SetName("Race creator").SetEmail("race-creator@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
					grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: "pricing.view", Scope: authorization.Assigned}, {Permission: kind + ".view", Scope: authorization.Assigned}, {Permission: kind + ".create", Scope: authorization.Assigned}, {Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All}}
					auth := authorization.New(pool)
					actor := authorization.Actor{UserID: admin}
					role, err := auth.SaveRole(ctx, actor, 0, "Race creator", grants)
					historyCheck(t, err)
					historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
					assignFieldDocumentCreator(t, pool, j.ID, u.ID)
					token, err := sessions.CreateMobile(ctx, u.ID, "Race creation")
					historyCheck(t, err)
					api := apiv1.NewRouter(pool, client, sessions)
					cookie := sessionCookie(t, ctx, sessions, admin)
					var mutate func() int
					switch change {
					case "job archive":
						mutate = func() int { return jobRepairRawPost(web, cookie, fmt.Sprintf("/jobs/%d/delete", j.ID), nil).Code }
					case "item archive":
						mutate = func() int { return jobRepairRawPost(web, cookie, fmt.Sprintf("/items/%d/delete", item.ID), nil).Code }
					case "customer archive":
						preview, err := auth.PreviewCustomerArchive(ctx, actor, c.ID)
						historyCheck(t, err)
						mutate = func() int {
							return jobRepairRawPost(web, cookie, fmt.Sprintf("/customers/%d/delete", c.ID), url.Values{"archive_snapshot": {preview.Snapshot}}).Code
						}
					case "customer drift":
						path := fmt.Sprintf("/jobs/%d", j.ID)
						values := commercialReviewedValues(t, web, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(other.ID)}, "job_type": {"Race work"}, "replace_customer": {"true"}})
						mutate = func() int { return jobRepairRawPost(web, cookie, path, values).Code }
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
						permission := "pricebook.view"
						if change == "pricing grant" {
							permission = "pricing.view"
						}
						next := []authorization.Grant{}
						for _, g := range grants {
							if g.Permission != permission {
								next = append(next, g)
							}
						}
						mutate = func() int {
							if _, err := auth.SaveRole(ctx, actor, role, "Race creator", next, 1); err != nil {
								return 409
							}
							return 303
						}
					}
					before := fieldCreationEffects(t, pool, company)
					operation := newOperationKey()
					body := json.RawMessage(fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Reviewed race","tax_rate":"0","line_items":[{"item_id":%d,"title":"Snapshot","quantity":"1","unit_price":"9.00","taxable":false}]}`, operation, newOperationKey(), j.ID, c.ID, item.ID))
					// Catalog drift is not authority drift and must not reprice the
					// retained editable selection at acceptance.
					client.Item.UpdateOneID(item.ID).SetUnitPrice(42).SaveX(ctx)
					barrier, err := pool.Begin(ctx)
					historyCheck(t, err)
					defer barrier.Rollback(context.Background())
					var holder, first int
					historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
					created := make(chan *httptest.ResponseRecorder, 1)
					changed := make(chan int, 1)
					runCreate := func() { created <- serviceAPIRequest(api, token, "POST", "/"+kind+"s", body) }
					runChange := func() { changed <- mutate() }
					if changeFirst {
						go runChange()
					} else {
						go runCreate()
					}
					commercialWaiters(t, ctx, pool, holder, 1)
					historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&first))
					if changeFirst {
						go runCreate()
					} else {
						go runChange()
					}
					commercialWaiters(t, ctx, pool, first, 1)
					historyCheck(t, barrier.Commit(ctx))
					w, status := <-created, <-changed
					wantChange := 303
					if change == "customer archive" || !changeFirst && (change == "job archive" || change == "customer drift") {
						wantChange = 409
					}
					if status != wantChange {
						t.Fatalf("change=%d want %d; create=%d %s", status, wantChange, w.Code, w.Body.String())
					}
					accepted := !changeFirst || change == "customer archive"
					if accepted {
						if w.Code != 200 {
							t.Fatalf("valid creation %d %s", w.Code, w.Body.String())
						}
						if replay := serviceAPIRequest(api, token, "POST", "/"+kind+"s", body); replay.Code != 200 || replay.Body.String() != w.Body.String() {
							t.Fatalf("accepted replay changed %d %s", replay.Code, replay.Body.String())
						}
						var count int
						historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM `+kind+`s WHERE customer_id=$1 AND job_id=$2`, c.ID, j.ID).Scan(&count))
						if count != 1 {
							t.Fatal("accepted creation retargeted/duplicated")
						}
						var captured string
						historyCheck(t, pool.QueryRow(ctx, `SELECT line_items::text FROM `+kind+`s WHERE customer_id=$1 AND job_id=$2`, c.ID, j.ID).Scan(&captured))
						assertContains(t, captured, `"item_id": `+fmt.Sprint(item.ID))
						assertNotContains(t, captured, "unit_cost")
						var total string
						historyCheck(t, pool.QueryRow(ctx, `SELECT financial::jsonb->'result'->>'total_cents' FROM `+kind+`s WHERE customer_id=$1 AND job_id=$2`, c.ID, j.ID).Scan(&total))
						if total != "900" {
							t.Fatalf("catalog drift repriced accepted snapshot: %s", total)
						}
					} else {
						if w.Code != 403 && w.Code != 404 && w.Code != 409 {
							t.Fatalf("losing creation %d %s", w.Code, w.Body.String())
						}
						if after := fieldCreationEffects(t, pool, company); after != before {
							t.Fatalf("partial rejected creation %s -> %s", before, after)
						}
						var acceptedReceipts int
						historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM commercial_operations WHERE operation_id=$1 AND receipt->>'outcome'='accepted'`, operation).Scan(&acceptedReceipts))
						if acceptedReceipts != 0 {
							t.Fatal("rejected creation has accepted receipt")
						}
					}
				})
			}
		}
	}
}
