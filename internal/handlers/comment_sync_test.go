package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/google/uuid"
)

func commentRevisionHTTP(t *testing.T, api http.Handler, token, kind string, id int64) string {
	t.Helper()
	r := httptest.NewRequest("GET", fmt.Sprintf("/%ss/%d/comments", kind, id), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("parent eligibility %d %s", w.Code, w.Body.String())
	}
	var result struct {
		Data struct {
			Revision string `json:"eligibility_revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Data.Revision == "" {
		t.Fatal("missing original eligibility witness")
	}
	return result.Data.Revision
}

func TestCommentSyncSnapshotsAndRemovalDeltasHTTP(t *testing.T) {
	f, api, token, _, _, jobs := syncFixture(t, 1)
	ctx := context.Background()
	service := authorization.New(f.pool)
	actor := authorization.Actor{UserID: f.admin}
	parent := objectref.New(objectref.TypeJob, jobs[0])
	id, err := service.AddComment(ctx, actor, parent, "Original\nplain <text>", 0)
	if err != nil {
		t.Fatal(err)
	}
	first := syncGet(t, api, token, "", 200)
	projection, ok := first.Records[0]["comments"].(map[string]any)
	if !ok {
		t.Fatalf("snapshot omits parent collaboration: %v", first.Records)
	}
	entries := projection["comments"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["content"] != "Original\nplain <text>" || projection["can_add"] != false {
		t.Fatalf("ordinary collaboration projection: %v", projection)
	}
	if err := service.RemoveComment(ctx, actor, parent, id); err != nil {
		t.Fatal(err)
	}
	delta := syncGet(t, api, token, "?cursor="+*first.Cursor, 200)
	if len(delta.Records) != 1 {
		t.Fatalf("removal omitted from delta: %v", delta)
	}
	removed := delta.Records[0]["comments"].(map[string]any)["comments"].([]any)[0].(map[string]any)
	if removed["content"] != "" || removed["removed_at"] == nil || removed["can_reply"] != false || fmt.Sprint(removed["id"]) != fmt.Sprint(entries[0].(map[string]any)["id"]) {
		t.Fatalf("placeholder lost identity/attribution: %v", removed)
	}
}

func TestCommentSyncBatchOriginalThreadAndDependenciesHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Comment batches").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Exact parent").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Comment batch")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	revision := commentRevisionHTTP(t, api, token, "job", job.ID)
	op := func(kind, text, reply, remove string, deps ...string) map[string]any {
		if deps == nil {
			deps = []string{}
		}
		intent := map[string]any{"parent_type": "job", "parent_revision": revision}
		if kind == "comment.add" {
			intent["content"] = text
		}
		if reply != "" {
			intent["reply_to"] = reply
		}
		if remove != "" {
			intent["remove_id"] = remove
		}
		return map[string]any{"operation_id": uuid.NewString(), "type": kind, "target": fmt.Sprint(job.ID), "depends_on": deps, "comment": intent}
	}
	submit := func(ops ...map[string]any) []struct {
		Receipt *authorization.CommentReceipt `json:"receipt"`
		Error   *struct {
			Code string `json:"code"`
		} `json:"error"`
	} {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"operations": ops})
		w := batchRequest(api, token, string(body))
		if w.Code != 200 {
			t.Fatalf("batch %d: %s", w.Code, w.Body.String())
		}
		var out struct {
			Data struct {
				Results []struct {
					Receipt *authorization.CommentReceipt `json:"receipt"`
					Error   *struct {
						Code string `json:"code"`
					} `json:"error"`
				} `json:"results"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data.Results
	}
	root := op("comment.add", "Original\n"+strings.Repeat("界", 9991), "", "")
	first := submit(root)[0].Receipt
	if first == nil || first.Code != "accepted" || first.CommentID == 0 {
		t.Fatalf("create: %+v", first)
	}
	if again := submit(root)[0].Receipt; again == nil || *again != *first {
		t.Fatalf("replay: %+v", again)
	}
	thread := fmt.Sprint(first.CommentID)
	reply := op("comment.add", "reply", thread, "")
	r := submit(reply)[0].Receipt
	if r == nil || r.Code != "accepted" {
		t.Fatalf("reply: %+v", r)
	}
	bad := op("comment.add", "nested", fmt.Sprint(r.CommentID), "")
	child := op("comment.add", "blocked", "", "", bad["operation_id"].(string))
	independent := op("comment.add", "independent", "", "")
	results := submit(bad, child, independent)
	for i, want := range []string{"comment_conflict", "failed_dependency", "accepted"} {
		if results[i].Receipt == nil || results[i].Receipt.Code != want {
			t.Fatalf("result%d want%s: %+v", i, want, results[i])
		}
	}
	remove := op("comment.remove", "", "", thread)
	if got := submit(remove)[0].Receipt; got == nil || got.Code != "accepted" {
		t.Fatalf("remove: %+v", got)
	}
	if got := submit(op("comment.remove", "", "", thread))[0].Receipt; got == nil || got.Code != "accepted" {
		t.Fatalf("no-op: %+v", got)
	}
	if got := submit(op("comment.add", "closed", thread, ""))[0].Receipt; got == nil || got.Code != "comment_conflict" {
		t.Fatalf("closed: %+v", got)
	}
	if got := submit(op("comment.add", strings.Repeat("界", 10001), "", ""))[0].Receipt; got == nil || got.Code != "invalid_comment" {
		t.Fatalf("bound: %+v", got)
	}
	root["comment"].(map[string]any)["content"] = "changed intent"
	if got := submit(root)[0]; got.Error == nil || got.Error.Code != "operation_intent_changed" {
		t.Fatalf("reuse: %+v", got)
	}
	// Original individual receipt remains the recovery seam after batch response loss.
	req := httptest.NewRequest("GET", "/comment-operations/"+first.OperationID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "Original") {
		t.Fatalf("safe receipt: %d %s", w.Code, w.Body.String())
	}
}

func TestCommentSyncDocumentScopeAndOpaqueOriginalParentHTTP(t *testing.T) {
	f, api, token, uid, _, jobs := syncFixture(t, 1)
	ctx := context.Background()
	service := authorization.New(f.pool)
	role, err := service.SaveRole(ctx, authorization.Actor{UserID: f.admin}, 0, "Comment field reader", []authorization.Grant{
		{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "estimate.view", Scope: authorization.Assigned},
		{Permission: "comment.add", Scope: authorization.Assigned}, {Permission: "comment.remove", Scope: authorization.Own},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AssignUser(ctx, authorization.Actor{UserID: f.admin}, uid, role); err != nil {
		t.Fatal(err)
	}
	var estimate int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,title) SELECT company_id,customer_id,id,'Original document' FROM jobs WHERE id=$1 RETURNING id`, jobs[0]).Scan(&estimate); err != nil {
		t.Fatal(err)
	}
	first := syncGet(t, api, token, "", 200)
	documents, ok := first.Records[0]["document_comments"].([]any)
	if !ok || len(documents) != 1 {
		t.Fatalf("authorized document collaboration missing: %v", first.Records)
	}
	doc := documents[0].(map[string]any)
	target, ok := doc["id"].(string)
	if !ok || target == fmt.Sprint(estimate) || doc["parent_type"] != "estimate" {
		t.Fatalf("stable opaque parent: %v", doc)
	}
	operation := map[string]any{"operation_id": uuid.NewString(), "type": "comment.add", "target": target, "depends_on": []string{}, "comment": map[string]any{"parent_type": "estimate", "content": "original document text", "parent_revision": doc["comments"].(map[string]any)["eligibility_revision"]}}
	body, _ := json.Marshal(map[string]any{"operations": []any{operation}})
	if w := batchRequest(api, token, string(body)); w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"accepted"`) {
		t.Fatalf("opaque original parent: %d %s", w.Code, w.Body.String())
	}
	delta := syncGet(t, api, token, "?cursor="+*first.Cursor, 200)
	entry := delta.Records[0]["document_comments"].([]any)[0].(map[string]any)["comments"].(map[string]any)["comments"].([]any)[0].(map[string]any)
	if _, ok := entry["id"].(string); !ok {
		t.Fatalf("opaque Comment identity: %v", entry)
	}
	if entry["content"] != "original document text" || entry["can_remove"] != true || entry["can_reply"] != true {
		t.Fatalf("own field capabilities: %v", entry)
	}
	// Losing the exact relationship invalidates old pages, without following text.
	if _, err := f.pool.Exec(ctx, `UPDATE estimates SET job_id=NULL WHERE id=$1`, estimate); err != nil {
		t.Fatal(err)
	}
	syncGet(t, api, token, "?cursor="+*first.Cursor, 409)
	operation["operation_id"] = uuid.NewString()
	body, _ = json.Marshal(map[string]any{"operations": []any{operation}})
	if w := batchRequest(api, token, string(body)); w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
		t.Fatalf("lost parent retargeted: %d %s", w.Code, w.Body.String())
	}
}

func TestCommentSyncBroadGrantsCannotWriteArchivedJobHTTP(t *testing.T) {
	f, api, _, _, _, jobs := syncFixture(t, 1)
	ctx := context.Background()
	token, err := f.sessions.CreateMobile(ctx, f.admin, "Broad Comment grants")
	if err != nil {
		t.Fatal(err)
	}
	var estimate int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,title) SELECT company_id,customer_id,id,'Archived Job document' FROM jobs WHERE id=$1 RETURNING id`, jobs[0]).Scan(&estimate); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, jobs[0]); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"operations":[{"operation_id":%q,"type":"comment.add","target":%q,"depends_on":[],"comment":{"parent_type":"estimate","content":"must not be accepted"}}]}`, uuid.NewString(), fmt.Sprint(estimate))
	w := batchRequest(api, token, body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
		t.Fatalf("broad grant bypassed active Job: %d %s", w.Code, w.Body.String())
	}
}
