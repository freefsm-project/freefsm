package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type offlineFileFixture struct {
	*associationRepairFixture
	api             http.Handler
	token           string
	user, role, job int64
	uploadDir       string
}

func fileSchema(t *testing.T, api http.Handler, name string, value any, valid bool) {
	t.Helper()
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc any
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("https://files.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("https://files.test/openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	err = s.Validate(v)
	if valid && err != nil || !valid && err == nil {
		t.Fatalf("schema %s valid=%v: %v", name, valid, err)
	}
}

func TestOfflineFilesBatchContractHTTP(t *testing.T) {
	f := offlineFiles(t)
	transfer, pub := f.transfer(t, "schema.txt", "contract bytes")
	op := fileBatchOp(pub, "file.publish", transfer, f.job)
	fileSchema(t, f.api, "OperationBatch", map[string]any{"operations": []any{op}}, true)
	results := f.batch(t, op)
	fileSchema(t, f.api, "OperationBatchEnvelope", map[string]any{"data": map[string]any{"results": results}}, true)
	page := f.call(t, "GET", "/sync/jobs", nil, 200)
	fileSchema(t, f.api, "JobSyncEnvelope", map[string]any{"data": page}, true)
	id := fmt.Sprint(results[0].(map[string]any)["receipt"].(map[string]any)["file_id"])
	meta := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	for _, kind := range []string{"file.rename", "file.remove"} {
		m := fileBatchOp(uuid.NewString(), kind, id, f.job)
		m["revision"] = meta["revision"]
		if kind == "file.rename" {
			m["name"] = "schema.txt"
		}
		fileSchema(t, f.api, "OperationBatch", map[string]any{"operations": []any{m}}, true)
		out := f.batch(t, m)
		fileSchema(t, f.api, "OperationBatchEnvelope", map[string]any{"data": map[string]any{"results": out}}, true)
	}
	for _, bad := range []string{`"bytes":"forbidden"`, `"depends_on":[null]`, `"object_id":"1"`, `"revision":"not a publication field"`, `"Occurred_at":"2026-01-01T00:00:00Z"`} {
		t.Run(bad, func(t *testing.T) {
			first := fileBatchOp(uuid.NewString(), "file.publish", transfer, f.job)
			invalid := fileBatchOp(uuid.NewString(), "file.publish", transfer, f.job)
			raw, _ := json.Marshal(invalid)
			var field map[string]any
			if err := json.Unmarshal([]byte("{"+bad+"}"), &field); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &invalid); err != nil {
				t.Fatal(err)
			}
			for k, v := range field {
				invalid[k] = v
			}
			envelope := map[string]any{"operations": []any{first, invalid}}
			fileSchema(t, f.api, "OperationBatch", envelope, false)
			f.call(t, "POST", "/sync/operations", envelope, 400)
			f.call(t, "GET", "/file-operations/"+first["operation_id"].(string), nil, 404)
		})
	}
}

func TestOfflineFilesOwnAuthorityDoesNotDiscloseHiddenParentsHTTP(t *testing.T) {
	f := offlineFiles(t)
	userToken := f.token
	token, err := f.sessions.CreateMobile(context.Background(), f.admin, "Office uploader")
	if err != nil {
		t.Fatal(err)
	}
	f.token = token
	transfer, pub := f.transfer(t, "other.txt", "other uploader")
	created := f.batch(t, fileBatchOp(pub, "file.publish", transfer, f.job))[0].(map[string]any)["receipt"].(map[string]any)
	id := fmt.Sprint(created["file_id"])
	f.token = userToken
	meta := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	if _, err := f.pool.Exec(context.Background(), `UPDATE authorization_grants SET scope='A' WHERE role_id=$1 AND permission IN ('file.rename','file.remove','file.download')`, f.role); err != nil {
		t.Fatal(err)
	}
	op := fileBatchOp(uuid.NewString(), "file.rename", id, f.job)
	op["revision"] = meta["revision"]
	op["name"] = "denied.txt"
	out := f.batch(t, op)[0].(map[string]any)["receipt"].(map[string]any)
	if out["code"] != "forbidden" {
		t.Fatalf("office grants expanded own field API: %v", out)
	}
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, f.user); err != nil {
		t.Fatal(err)
	}
	op["operation_id"] = uuid.NewString()
	out = f.batch(t, op)[0].(map[string]any)["receipt"].(map[string]any)
	if out["code"] != "not_found" {
		t.Fatalf("invisible parent disclosed uploader/action: %v", out)
	}
}

func offlineFiles(t *testing.T) *offlineFileFixture {
	t.Helper()
	f, _, token, uid, role, jobs := syncFixture(t, 1)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'file.upload','J'),($1,'file.download','J'),($1,'file.rename','O'),($1,'file.remove','O'),($1,'job.field_notes.edit','J')`, role); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	files := services.NewFileService(f.client, objectref.NewEntDirectory(f.client), root, 1024)
	return &offlineFileFixture{associationRepairFixture: f, api: apiv1.NewRouter(f.pool, f.client, f.sessions, files), token: token, user: uid, role: role, job: jobs[0], uploadDir: root}
}

func (f *offlineFileFixture) call(t *testing.T, method, path string, body any, status int) map[string]any {
	t.Helper()
	var data string
	if raw, ok := body.(string); ok {
		data = raw
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		data = string(b)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(data))
	r.Header.Set("Authorization", "Bearer "+f.token)
	r.Header.Set("Content-Type", "application/json")
	stageTestFileTransport(r, data)
	w := httptest.NewRecorder()
	f.api.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if d, ok := out["data"].(map[string]any); ok {
		return d
	}
	return out
}

func (f *offlineFileFixture) transfer(t *testing.T, name, content string) (string, string) {
	return f.transferAt(t, "job", f.job, name, content)
}

func (f *offlineFileFixture) transferAt(t *testing.T, kind string, parent int64, name, content string) (string, string) {
	t.Helper()
	id, publication := uuid.NewString(), uuid.NewString()
	digest := sha256.Sum256([]byte(content))
	f.call(t, "POST", "/file-transfers", map[string]any{"id": id, "publication_id": publication, "object_type": kind, "object_id": parent, "name": name, "size": len(content), "sha256": hex.EncodeToString(digest[:])}, 201)
	f.call(t, "PUT", "/file-transfers/"+id+"/chunks?offset=0", content, 200)
	return id, publication
}

func fileBatchOp(id, kind, target string, parent int64) map[string]any {
	return map[string]any{"operation_id": id, "type": kind, "target": target, "object_type": "job", "object_id": parent, "depends_on": []string{}}
}

func (f *offlineFileFixture) batch(t *testing.T, ops ...map[string]any) []any {
	t.Helper()
	return f.call(t, "POST", "/sync/operations", map[string]any{"operations": ops}, 200)["results"].([]any)
}

func TestOfflineFilesBatchPublicationHTTP(t *testing.T) {
	f := offlineFiles(t)
	transfer, publication := f.transfer(t, "published.txt", "verified publication")
	before := syncGet(t, f.api, f.token, "", 200)
	if len(before.Records[0]["files"].([]any)) != 0 {
		t.Fatal("bytes published without command")
	}
	op := fileBatchOp(publication, "file.publish", transfer, f.job)
	result := f.batch(t, op)[0].(map[string]any)
	receipt, ok := result["receipt"].(map[string]any)
	if !ok || receipt["code"] != "accepted" || receipt["file_id"] == nil {
		t.Fatalf("publication: %v", result)
	}
	replay := f.batch(t, op)[0].(map[string]any)["receipt"].(map[string]any)
	if fmt.Sprint(replay) != fmt.Sprint(receipt) {
		t.Fatalf("replay changed receipt: %v %v", receipt, replay)
	}
	page := syncGet(t, f.api, f.token, "?cursor="+*before.Cursor, 200)
	if len(page.Records) != 1 || len(page.Records[0]["files"].([]any)) != 1 {
		t.Fatalf("create-once effect: %+v", page)
	}
	if _, err := f.pool.Exec(context.Background(), `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, f.user); err != nil {
		t.Fatal(err)
	}
	safe := f.call(t, "GET", "/file-operations/"+publication, nil, 200)
	if safe["file_id"] != receipt["file_id"] || strings.Contains(fmt.Sprint(safe), "published.txt") {
		t.Fatalf("scope-loss receipt: %v", safe)
	}
	replay = f.batch(t, op)[0].(map[string]any)["receipt"].(map[string]any)
	if fmt.Sprint(replay) != fmt.Sprint(receipt) {
		t.Fatalf("scope-loss replay: %v", replay)
	}
}

func TestOfflineFilesBatchMutationsAndDependenciesHTTP(t *testing.T) {
	f := offlineFiles(t)
	transfer, publication := f.transfer(t, "original.txt", "fixed bytes")
	pub := f.batch(t, fileBatchOp(publication, "file.publish", transfer, f.job))[0].(map[string]any)["receipt"].(map[string]any)
	id := fmt.Sprint(pub["file_id"])
	metadata := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	mutation := func(kind, name, revision string) map[string]any {
		op := fileBatchOp(uuid.NewString(), kind, id, f.job)
		op["revision"] = revision
		if kind == "file.rename" {
			op["name"] = name
		}
		return op
	}
	initial := metadata["revision"].(string)
	rename := mutation("file.rename", "renamed.txt", initial)
	rename["depends_on"] = []string{publication}
	failed := mutation("file.rename", "stale.txt", initial)
	blocked := mutation("file.remove", "", initial)
	blocked["depends_on"] = []string{failed["operation_id"].(string)}
	noop := mutation("file.rename", "renamed.txt", initial)
	results := f.batch(t, rename, failed, blocked, noop)
	for i, want := range []string{"accepted", "file_conflict", "failed_dependency", "accepted"} {
		r, ok := results[i].(map[string]any)["receipt"].(map[string]any)
		if !ok || r["code"] != want {
			t.Fatalf("result%d want%s: %v", i, want, results[i])
		}
	}
	metadata = f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	if metadata["name"] != "renamed.txt" || metadata["revision"] == initial {
		t.Fatalf("rename: %v", metadata)
	}
	back := mutation("file.rename", "original.txt", metadata["revision"].(string))
	f.batch(t, back)
	aba := mutation("file.rename", "ABA.txt", initial)
	badExtension := mutation("file.rename", "original.pdf", metadata["revision"].(string))
	for i, result := range f.batch(t, aba, badExtension) {
		want := []string{"file_conflict", "invalid_file"}[i]
		if result.(map[string]any)["receipt"].(map[string]any)["code"] != want {
			t.Fatalf("guard: %v", result)
		}
	}
	metadata = f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	remove := mutation("file.remove", "", metadata["revision"].(string))
	if f.batch(t, remove)[0].(map[string]any)["receipt"].(map[string]any)["code"] != "accepted" {
		t.Fatal("remove failed")
	}
	removed := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	f.batch(t, mutation("file.remove", "", initial))
	unchanged := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	if fmt.Sprint(removed) != fmt.Sprint(unchanged) {
		t.Fatalf("no-op changed removal attribution: %v %v", removed, unchanged)
	}
	f.call(t, "GET", "/files/"+id, nil, 404)
	var renames, removes int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE action='file.rename'),count(*) FILTER(WHERE action='file.remove') FROM authorization_audit WHERE target_type='file' AND target_id=$1`, id).Scan(&renames, &removes); err != nil {
		t.Fatal(err)
	}
	if renames != 2 || removes != 1 {
		t.Fatalf("no-op/failed effects reattributed history: rename%d remove%d", renames, removes)
	}
}

func TestOfflineFilesInstalledIdentityReceiptPrecisionHTTP(t *testing.T) {
	f := offlineFiles(t)
	ctx := context.Background()
	legacy := f.client.File.Create().SetID(9007199254740993).SetCompanyID(f.company).SetObjectType("job").SetObjectID(f.job).SetOriginalName("recorded.txt").SetStoredName("recorded.txt").SetMimeType("text/plain").SetFileSize(12).SetFilePath("/recorded/legacy/path").SetUploadedBy(f.user).SaveX(ctx)
	page := syncGet(t, f.api, f.token, "", 200)
	entry := page.Records[0]["files"].([]any)[0].(map[string]any)
	if entry["id"] != "9007199254740993" || entry["publication_id"] != nil {
		t.Fatalf("invented or rounded installed identity: %v", entry)
	}
	op := fileBatchOp(uuid.NewString(), "file.rename", fmt.Sprint(legacy.ID), f.job)
	op["revision"] = entry["revision"]
	op["name"] = "recorded.txt"
	receipt := f.batch(t, op)[0].(map[string]any)["receipt"].(map[string]any)
	if receipt["effect_id"] != "9007199254740993" {
		t.Fatalf("receipt has no precision-safe retained effect identity: %v", receipt)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, f.user); err != nil {
		t.Fatal(err)
	}
	safe := f.call(t, "GET", "/file-operations/"+op["operation_id"].(string), nil, 200)
	if safe["effect_id"] != "9007199254740993" || strings.Contains(fmt.Sprint(safe), "recorded.txt") {
		t.Fatalf("scope-loss identity: %v", safe)
	}
}

func TestOfflineFilesUnavailableServiceDoesNotBlockUnrelatedHTTP(t *testing.T) {
	f := offlineFiles(t)
	transfer, pub := f.transfer(t, "available.txt", "available bytes")
	notes := f.call(t, "GET", fmt.Sprintf("/jobs/%d/field-notes", f.job), nil, 200)
	op := fileBatchOp(pub, "file.publish", transfer, f.job)
	good := map[string]any{"operation_id": uuid.NewString(), "type": "job.field_notes", "target": fmt.Sprint(f.job), "depends_on": []string{}, "notes_revision": notes["notes_revision"], "field_notes": "unrelated survives"}
	// The production router supports optional File storage dependencies. A
	// missing service must be a per-operation retry, never panic the whole batch.
	withFiles := f.api
	f.api = apiv1.NewRouter(f.pool, f.client, f.sessions)
	results := f.batch(t, op, good)
	first := results[0].(map[string]any)
	if first["receipt"] != nil || first["error"].(map[string]any)["code"] != "retryable_failure" {
		t.Fatalf("missing dependency disposition: %v", first)
	}
	if results[1].(map[string]any)["receipt"].(map[string]any)["code"] != "accepted" {
		t.Fatalf("unrelated blocked: %v", results[1])
	}
	f.api = withFiles
	f.call(t, "GET", "/file-operations/"+pub, nil, 404)
	if r := f.batch(t, op)[0].(map[string]any)["receipt"].(map[string]any); r["code"] != "accepted" {
		t.Fatalf("same-ID dependency recovery: %v", r)
	}
}
