package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/google/uuid"
)

func TestCommentSyncEligibilityParentKindsHTTP(t *testing.T) {
	for _, kind := range []string{"job", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Eligibility parent kinds").SaveX(ctx)
			var completed int64
			if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' ORDER BY id LIMIT 1`, company).Scan(&completed); err != nil {
				t.Fatal(err)
			}
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Finished active parent").SetStatusID(completed).SaveX(ctx)
			parent := job.ID
			if kind == "invoice" {
				if err := pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,title,invoice_number,status_id) VALUES($1,$2,$3,'Void eligible parent',84001,(SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.company_id=$1 AND w.object_type='invoice' AND lower(s.name)='void' LIMIT 1)) RETURNING id`, company, customer.ID, job.ID).Scan(&parent); err != nil {
					t.Fatal(err)
				}
			}
			token, err := sessions.CreateMobile(ctx, admin, "Parent eligibility")
			if err != nil {
				t.Fatal(err)
			}
			api := apiv1.NewRouter(pool, client, sessions)
			cookie := sessionCookie(t, ctx, sessions, admin)
			revision := commentRevisionHTTP(t, api, token, kind, parent)
			path := fmt.Sprintf("/%ss/%d/comments", kind, parent)
			call := func(body map[string]any, want int) map[string]any {
				t.Helper()
				raw, _ := json.Marshal(body)
				r := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				if w.Code != want {
					t.Fatalf("%s %d want%d: %s", kind, w.Code, want, w.Body.String())
				}
				var value map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
					t.Fatal(err)
				}
				return value
			}
			rootBody := map[string]any{"operation_id": uuid.NewString(), "parent_revision": revision, "content": "root"}
			root := call(rootBody, 200)["data"].(map[string]any)
			pending := map[string]any{"operation_id": uuid.NewString(), "parent_revision": revision, "content": "original staged reply", "reply_to": root["comment_id"]}
			base := fmt.Sprintf("/%ss/%d", kind, parent)
			commentPost(t, web, cookie, base+"/delete", nil, 303)
			commentPost(t, web, cookie, base+"/restore", nil, 303)
			if code := call(pending, 409)["error"].(map[string]any)["code"]; code != "comment_parent_changed" {
				t.Fatalf("lifecycle guard %v", code)
			}
			if got := call(rootBody, 200)["data"].(map[string]any); !reflect.DeepEqual(got, root) {
				t.Fatal("original terminal receipt changed")
			}
			fresh := commentRevisionHTTP(t, api, token, kind, parent)
			if fresh == revision {
				t.Fatal("lifecycle ABA did not advance eligibility")
			}
			pending["parent_revision"] = fresh
			call(pending, 409) // new current eligibility cannot rehabilitate a terminal ID
			pending["operation_id"] = uuid.NewString()
			call(pending, 200)
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE object_type=$1 AND object_id=$2`, kind, parent).Scan(&count); err != nil || count != 2 {
				t.Fatalf("stale/replay duplicated effects: %d %v", count, err)
			}
		})
	}
}
