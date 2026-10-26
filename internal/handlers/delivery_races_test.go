package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/delivery"
)

func TestDeliveryObservedQueueMutationOrderHTTP(t *testing.T) {
	for _, change := range []string{"archive", "remove", "rename", "convert", "revoke", "revoke-send", "revoke-download", "revoke-file"} {
		for _, queueFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/queue_first_%v", change, queueFirst), func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				adminCookie := sessionCookie(t, ctx, sessions, admin)
				cookie := adminCookie
				customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race customer").SaveX(ctx)
				doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Original document").SaveX(ctx)
				path := fmt.Sprintf("/estimates/%d", doc.ID)
				_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
				historyCheck(t, err)
				conversionUpload(t, web, adminCookie, "estimate", doc.ID, "original.txt", "immutable original File bytes")
				var fileID int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM files WHERE object_type='estimate' AND object_id=$1`, doc.ID).Scan(&fileID))
				expectStatus(t, web, adminCookie, "POST", path+"/pdf/save", 303)
				version := compositionInput(t, requestBody(t, web, adminCookie, "GET", path+"/pdf/preview", 200), "version")
				queue := creditCommand{path + "/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"one@example.test,two@example.test"}, "subject": {"Reviewed"}, "body": {"Message"}, "extra_file_id": {fmt.Sprint(fileID)}}}
				var mutation creditCommand
				var barrierEvent string
				switch change {
				case "archive":
					mutation = creditCommand{path + "/delete", url.Values{}}
					barrierEvent = "UPDATE OF deleted_at ON estimates"
				case "remove":
					mutation = creditCommand{fmt.Sprintf("/files/%d/delete", fileID), url.Values{"revision": {"1"}}}
					barrierEvent = "UPDATE OF removed_at ON files"
				case "rename":
					mutation = creditCommand{fmt.Sprintf("/files/%d/rename", fileID), url.Values{"revision": {"1"}, "original_name": {"renamed.txt"}}}
					barrierEvent = "UPDATE OF original_name ON files"
				case "convert":
					mutation = creditCommand{path + "/convert", conversionReview(t, web, adminCookie, path+"/convert")}
					barrierEvent = "UPDATE OF conversion_hidden_at ON estimates"
				case "revoke", "revoke-send", "revoke-download", "revoke-file":
					auth := authorization.New(pool)
					actor := authorization.Actor{UserID: admin}
					role, err := auth.SaveRole(ctx, actor, 0, "Queue before revocation", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.delivery.request", Scope: authorization.All}, {Permission: "estimate.pdf.download", Scope: authorization.All}, {Permission: "file.download", Scope: authorization.All}})
					historyCheck(t, err)
					var remaining []authorization.Grant
					if change != "revoke" {
						missing := map[string]string{"revoke-send": "estimate.delivery.request", "revoke-download": "estimate.pdf.download", "revoke-file": "file.download"}[change]
						for _, permission := range []string{"estimate.view", "estimate.delivery.request", "estimate.pdf.download", "file.download"} {
							if permission != missing {
								remaining = append(remaining, authorization.Grant{Permission: permission, Scope: authorization.All})
							}
						}
					}
					empty, err := auth.SaveRole(ctx, actor, 0, "Changed Delivery authority", remaining)
					historyCheck(t, err)
					user := client.User.Create().SetCompanyID(company).SetEmail("race-sender@test").SetName("Sender").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
					historyCheck(t, auth.AssignUser(ctx, actor, user.ID, role))
					cookie = sessionCookie(t, ctx, sessions, user.ID)
					mutation = creditCommand{"/settings/authorization/assign", url.Values{"user_id": {fmt.Sprint(user.ID)}, "role_id": {fmt.Sprint(empty)}}}
					barrierEvent = "UPDATE OF authorization_role_id ON users"
				}
				mixed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == mutation.path {
						r.Header.Del("Cookie")
						r.AddCookie(adminCookie)
					}
					web.ServeHTTP(w, r)
				})
				f := &creditMatrix{ctx: ctx, pool: pool, web: mixed, cookie: cookie}
				a, b, event := queue, mutation, "INSERT ON document_deliveries"
				if !queueFirst {
					a, b, event = mutation, queue, barrierEvent
				}
				ra, rb := conversionOrderedEffects(t, f, a, b, event)
				queued, changed := ra, rb
				if !queueFirst {
					queued, changed = rb, ra
				}
				wantMutation := 303
				if change == "remove" || change == "rename" {
					wantMutation = 200
				}
				if changed.Code != wantMutation {
					t.Fatalf("mutation=%d %s", changed.Code, changed.Body.String())
				}
				accepted := queueFirst || change == "rename"
				if accepted && queued.Code != 303 || !accepted && queued.Code != 403 && queued.Code != 404 {
					t.Fatalf("queue=%d %s", queued.Code, queued.Body.String())
				}
				history, err := delivery.New(pool, "").History(ctx, company, delivery.DocumentRef{Type: "estimate", ID: doc.ID})
				historyCheck(t, err)
				if len(history) != 2*boolInt(accepted) {
					t.Fatalf("partial fanout: %+v", history)
				}
				for _, row := range history {
					want := "queued"
					if change == "archive" || change == "convert" {
						want = "cancelled"
					}
					if row.State != want {
						t.Fatalf("unexpected queue lifecycle=%+v", row)
					}
				}
				if accepted {
					var raw []byte
					historyCheck(t, pool.QueryRow(ctx, `SELECT attachment_snapshots FROM document_deliveries ORDER BY id LIMIT 1`).Scan(&raw))
					var files []authorization.DeliveryFile
					historyCheck(t, json.Unmarshal(raw, &files))
					name := "original.txt"
					if change == "rename" && !queueFirst {
						name = "renamed.txt"
					}
					if len(files) != 1 || files[0].ID != fileID || files[0].Name != name || string(files[0].Data) != "immutable original File bytes" {
						t.Fatalf("retargeted attachment: %+v", files)
					}
				}
			})
		}
	}
}
