package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
)

func TestOfflineFilesSnapshotPublicationAndRemoval(t *testing.T) {
	f, _, token, uid, role, jobs := syncFixture(t, 1)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'file.upload','J'),($1,'file.download','J'),($1,'file.rename','O'),($1,'file.remove','O')`, role); err != nil {
		t.Fatal(err)
	}
	files := services.NewFileService(f.client, objectref.NewEntDirectory(f.client), t.TempDir(), 1024)
	api := apiv1.NewRouter(f.pool, f.client, f.sessions, files)
	call := func(method, path, body string) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		stageTestFileTransport(r, body)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 && w.Code != 201 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		var out struct{ Data map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data
	}
	transfer, publication := uuid.NewString(), uuid.NewString()
	content := "offline File bytes"
	digest := sha256.Sum256([]byte(content))
	call("POST", "/file-transfers", fmt.Sprintf(`{"id":%q,"publication_id":%q,"object_type":"job","object_id":%d,"name":"field.txt","size":%d,"sha256":%q}`, transfer, publication, jobs[0], len(content), hex.EncodeToString(digest[:])))
	call("PUT", "/file-transfers/"+transfer+"/chunks?offset=0", content)
	call("POST", "/file-transfers/"+transfer+"/verify", "")
	before := syncGet(t, api, token, "", 200)
	if entries, ok := before.Records[0]["files"].([]any); !ok || len(entries) != 0 {
		t.Fatalf("verified transfer must have an empty ordinary File projection: %v", before.Records[0]["files"])
	}
	created := call("POST", "/file-transfers/"+transfer+"/publish", "")
	page := syncGet(t, api, token, "?cursor="+*before.Cursor, 200)
	if len(page.Records) != 1 {
		t.Fatalf("publication missing from delta: %+v", page)
	}
	entries, ok := page.Records[0]["files"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("published File projection: %v", page.Records[0]["files"])
	}
	metadata := entries[0].(map[string]any)
	if metadata["name"] != "field.txt" || metadata["extension"] != ".txt" || metadata["publication_id"] != publication || metadata["uploaded_by"] != fmt.Sprint(uid) || metadata["revision"] != created["revision"] {
		t.Fatalf("File metadata: %v", metadata)
	}
	capabilities := metadata["capabilities"].(map[string]any)
	if capabilities["download"] != true || capabilities["preview"] != false || capabilities["rename"] != true || capabilities["remove"] != true {
		t.Fatalf("independent File capabilities: %v", capabilities)
	}
	call("POST", fmt.Sprintf("/files/%.0f/remove", created["id"]), fmt.Sprintf(`{"operation_id":%q,"object_type":"job","object_id":%d,"revision":%q}`, uuid.NewString(), jobs[0], created["revision"]))
	removed := syncGet(t, api, token, "?cursor="+*page.Cursor, 200)
	if len(removed.Records) != 1 {
		t.Fatalf("removal missing from delta: %+v", removed)
	}
	metadata = removed.Records[0]["files"].([]any)[0].(map[string]any)
	if metadata["removed_at"] == nil || metadata["removed_by"] != fmt.Sprint(uid) || metadata["name"] != "field.txt" || metadata["capabilities"].(map[string]any)["download"] != false {
		t.Fatalf("retained removal metadata: %v", metadata)
	}
	// A stored successor must not replay File metadata after its original
	// parent leaves this client's scope, even though the Job remains visible.
	job := f.client.Job.GetX(ctx, jobs[0])
	other := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(job.CustomerID).SetJobType("Inaccessible parent").SaveX(ctx)
	if _, err := f.pool.Exec(ctx, `UPDATE files SET object_id=$1,revision=revision+1 WHERE id=$2`, other.ID, created["id"]); err != nil {
		t.Fatal(err)
	}
	continuityResnapshot(t, continuityRequest(api, token, "?cursor="+*before.Cursor))
}
