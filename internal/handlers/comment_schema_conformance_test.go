package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCommentSyncDirectOpenAPIHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Direct schema owner").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Direct schema parent").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Direct schema")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var spec map[string]any
	if err := json.Unmarshal(document.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://direct-comments.test/openapi.json", spec); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"CommentCommand", "CommentRemoveCommand", "CommentsEnvelope", "CommentReceiptEnvelope", "ErrorEnvelope"} {
		schema, err := compiler.Compile("https://direct-comments.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		schemas[name] = schema
	}
	validate := func(name string, raw []byte, valid bool) {
		t.Helper()
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		err = schemas[name].Validate(value)
		if (err == nil) != valid {
			t.Fatalf("%s schema valid=%v: %v", name, valid, err)
		}
	}
	call := func(method, path, requestSchema, responseSchema string, body map[string]any, want int, requestValid, authorized bool) map[string]any {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
			validate(requestSchema, raw, requestValid)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		if authorized {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s status=%d want%d: %s", method, path, w.Code, want, w.Body.String())
		}
		validate(responseSchema, w.Body.Bytes(), true)
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	base := fmt.Sprintf("/jobs/%d/comments", job.ID)
	call("GET", base, "", "ErrorEnvelope", nil, 401, true, false)
	list := call("GET", base, "", "CommentsEnvelope", nil, 200, true, true)["data"].(map[string]any)
	revision := list["eligibility_revision"].(string)
	root := map[string]any{"operation_id": uuid.NewString(), "parent_revision": revision, "content": "plain <b>\nsecond line"}
	first := call("POST", base, "CommentCommand", "CommentReceiptEnvelope", root, 200, true, true)["data"].(map[string]any)
	rootID := int64(first["comment_id"].(float64))
	reply := map[string]any{"operation_id": uuid.NewString(), "parent_revision": revision, "content": "reply", "reply_to": rootID}
	replied := call("POST", base, "CommentCommand", "CommentReceiptEnvelope", reply, 200, true, true)["data"].(map[string]any)
	replyID := int64(replied["comment_id"].(float64))
	for _, id := range []int64{replyID, rootID} {
		remove := map[string]any{"operation_id": uuid.NewString(), "parent_revision": revision}
		call("POST", fmt.Sprintf("%s/%d/remove", base, id), "CommentRemoveCommand", "CommentReceiptEnvelope", remove, 200, true, true)
		call("GET", base, "", "CommentsEnvelope", nil, 200, true, true)
	}
	late := map[string]any{"operation_id": uuid.NewString(), "parent_revision": revision, "content": "closed", "reply_to": rootID}
	call("POST", base, "CommentCommand", "ErrorEnvelope", late, 409, true, true)
	call("GET", "/comment-operations/"+late["operation_id"].(string), "", "CommentReceiptEnvelope", nil, 200, true, true)
	invalid := map[string]any{"operation_id": uuid.NewString(), "parent_revision": revision, "content": strings.Repeat("x", 10001)}
	call("POST", base, "CommentCommand", "ErrorEnvelope", invalid, 400, false, true)
	missingWitness := map[string]any{"operation_id": uuid.NewString(), "content": "new unguarded intent"}
	call("POST", base, "CommentCommand", "ErrorEnvelope", missingWitness, 409, true, true) // wire omission exists only for old terminal replay
	other := client.User.Create().SetCompanyID(company).SetName("Original other author").SetEmail("schema-other@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	var foreign int64
	if err := pool.QueryRow(ctx, `INSERT INTO comments(company_id,object_type,object_id,author_id,content) VALUES($1,'job',$2,$3,'other author') RETURNING id`, company, job.ID, other.ID).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	call("POST", fmt.Sprintf("%s/%d/remove", base, foreign), "CommentRemoveCommand", "ErrorEnvelope", map[string]any{"operation_id": uuid.NewString(), "parent_revision": revision}, 403, true, true)
	call("GET", "/jobs/9223372036854775807/comments", "", "ErrorEnvelope", nil, 404, true, true)
	call("POST", base, "CommentCommand", "ErrorEnvelope", map[string]any{"operation_id": uuid.NewString(), "parent_revision": revision, "content": "unknown property", "edit": true}, 400, false, true)
	t.Log("served direct Comment create/reply/remove/collection/receipt/error schemas validated through production HTTP, including401/403/404/409/400")
}
