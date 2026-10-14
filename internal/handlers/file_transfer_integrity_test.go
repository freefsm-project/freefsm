package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestTransferVerifiedWithoutPublicationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	authority := authorization.New(pool)
	role, err := authority.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Transfer-only role", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "file.upload", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("transfer@test").SetPasswordHash("hash").SetName("Transfer user").SetRole("tech").SaveX(ctx)
	if err := authority.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Transfer customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Transfer work").SaveX(ctx)
	files := services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 11)
	api := apiv1.NewRouter(pool, client, sessions, files)
	redeemed := pairingCall(api, "/pairings/redeem", approvedPairing(t, api, web, sessionCookie(t, ctx, sessions, u.ID)))
	var bearer struct{ Data struct{ Token string } }
	if err := json.Unmarshal(redeemed.Body.Bytes(), &bearer); err != nil || redeemed.Code != 201 {
		t.Fatalf("pairing: %d %v", redeemed.Code, err)
	}
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc any
	if err := json.Unmarshal(document.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://transfer.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	validate := func(name string, body []byte) {
		t.Helper()
		schema, err := compiler.Compile("https://transfer.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	call := func(method, path, body string, status int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+bearer.Data.Token)
		r.Header.Set("Content-Type", "application/json")
		stageTestFileTransport(r, body)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		contractPath := strings.Split(path, "?")[0]
		if strings.HasPrefix(contractPath, "/file-transfers/") && contractPath != "/file-transfers/lookup" {
			parts := strings.Split(contractPath, "/")
			parts[2] = "{transfer}"
			contractPath = strings.Join(parts, "/")
		}
		operation := doc.(map[string]any)["paths"].(map[string]any)[contractPath].(map[string]any)[strings.ToLower(method)].(map[string]any)
		responses := operation["responses"].(map[string]any)
		response, found := responses[fmt.Sprint(status)]
		key := fmt.Sprint(status)
		if !found {
			response = responses["default"]
			key = "default"
		}
		if response == nil {
			t.Fatalf("undocumented status %s %s %d", method, contractPath, status)
		}
		fragment := "/paths/" + strings.ReplaceAll(contractPath, "/", "~1") + "/" + strings.ToLower(method) + "/responses/" + key
		if ref, ok := response.(map[string]any)["$ref"].(string); ok {
			fragment = strings.TrimPrefix(ref, "#")
		}
		schema, err := compiler.Compile("https://transfer.test/openapi.json#" + fragment + "/content/application~1json/schema")
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(w.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatalf("response contract %s: %v", fragment, err)
		}
		return w.Body.String()
	}
	// JSON property names are case-sensitive in the served schema, including
	// when a canonical property is present alongside an unknown case variant.
	rejectCaseVariants := func(path, fragment, body string) {
		t.Helper()
		schema, err := compiler.Compile("https://transfer.test/openapi.json#" + fragment)
		if err != nil {
			t.Fatal(err)
		}
		var canonical map[string]any
		if err := json.Unmarshal([]byte(body), &canonical); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(canonical); err != nil {
			t.Fatalf("canonical request fails schema: %v", err)
		}
		snapshot := func() [4]int64 {
			t.Helper()
			var counts [4]int64
			if err := pool.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM file_transfers),
				(SELECT count(*) FROM file_transfer_chunks),
				(SELECT count(*) FROM authorization_audit WHERE detail ? 'transfer_id'),
				(SELECT count(*) FROM files)`).Scan(&counts[0], &counts[1], &counts[2], &counts[3]); err != nil {
				t.Fatal(err)
			}
			return counts
		}
		for key := range canonical {
			for _, extra := range []bool{false, true} {
				t.Logf("rejecting %s property %s (extra=%t)", path, key, extra)
				request := make(map[string]any, len(canonical)+1)
				for k, v := range canonical {
					request[k] = v
				}
				request[strings.ToUpper(key)] = request[key]
				if !extra {
					delete(request, key)
				}
				if err := schema.Validate(request); err == nil {
					t.Fatal("case-variant property unexpectedly satisfies schema")
				}
				encoded, err := json.Marshal(request)
				if err != nil {
					t.Fatal(err)
				}
				before := snapshot()
				response := call("POST", path, string(encoded), 400)
				if !strings.Contains(response, `"code":"invalid_json"`) {
					t.Fatalf("expected typed invalid_json: %s", response)
				}
				if after := snapshot(); after != before {
					t.Fatalf("invalid request committed effects: %v -> %v", before, after)
				}
			}
		}
	}
	sum := sha256.Sum256([]byte("hello world"))
	id := uuid.NewString()
	start := fmt.Sprintf(`{"id":%q,"publication_id":%q,"object_type":"job","object_id":%d,"name":"field.txt","size":11,"sha256":%q}`, id, uuid.NewString(), j.ID, hex.EncodeToString(sum[:]))
	validate("TransferRequest", []byte(start))
	withOptionals := strings.TrimSuffix(start, "}") + `,"transferred":0,"verified":false,"occurred_at":"2026-01-01T00:00:00Z"}`
	rejectCaseVariants("/file-transfers", "/components/schemas/TransferRequest", withOptionals)
	if limits := call("GET", "/files/limits", "", 200); !strings.Contains(limits, `"max_file_bytes":11`) {
		t.Fatal("configured limit undiscoverable")
	}
	call("POST", "/file-transfers", strings.Replace(start, `"size":11`, `"size":12`, 1), 400)
	call("POST", "/file-transfers", start, 201)
	call("GET", "/file-transfers/"+id+"?expand=secret", "", 400)
	call("PUT", "/file-transfers/"+id+"/chunks?offset=0&offset=1", "hello ", 400)
	call("PUT", "/file-transfers/"+id+"/chunks?offset=0", "hello ", 200)
	call("POST", "/file-transfers/"+id+"/verify", "", 409)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		cmd := exec.Command(binary, "-test.run=^TestTransferRestartProbe$", "-test.v")
		cmd.Env = append(os.Environ(), "FREEFSM_TRANSFER_PROBE_DSN="+pool.Config().ConnString(), "FREEFSM_TRANSFER_PROBE_TOKEN="+bearer.Data.Token, "FREEFSM_TRANSFER_PROBE_ID="+id)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("process restart: %v %s", err, output)
		}
	}
	// A new router/service over the same persisted database has no process cache.
	api = apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewEntDirectory(client), files.UploadDir(), 11))
	call("PUT", "/file-transfers/"+id+"/chunks?offset=0", "hello ", 200)
	call("PUT", "/file-transfers/"+id+"/chunks?offset=6", "world", 200)
	verified := call("POST", "/file-transfers/"+id+"/verify", "", 200)
	if !strings.Contains(verified, `"verified":true`) {
		t.Fatalf("no independent verified completion: %s", verified)
	}
	if n := client.File.Query().CountX(ctx); n != 0 {
		t.Fatalf("temporary completion published %d Files", n)
	}
	call("POST", "/file-transfers/"+id+"/verify", "", 200)
	// Immutable transport audit and bytes commit together; retries have no new
	// effects. This is protected audit, not ordinary collaboration history.
	var events int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE detail->>'transfer_id'=$1`, id).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 3 {
		t.Fatalf("start + two durable segments must have three audit facts, got %d", events)
	}
	lookup := fmt.Sprintf(`{"object_type":"job","object_id":%d,"sha256":%q}`, j.ID, hex.EncodeToString(sum[:]))
	if found := call("POST", "/file-transfers/lookup", lookup, 200); !strings.Contains(found, id) {
		t.Fatal("own verified digest not reusable")
	}
	rejectCaseVariants("/file-transfers/lookup", "/paths/~1file-transfers~1lookup/post/requestBody/content/application~1json/schema", lookup)
	other, err := sessions.CreateMobile(ctx, admin, "Another device")
	if err != nil {
		t.Fatal(err)
	}
	old := bearer.Data.Token
	bearer.Data.Token = other
	call("GET", "/file-transfers/"+id, "", 404)
	call("POST", "/file-transfers/lookup", lookup, 404)
	// Starting the same caller-chosen UUID must not probe another user's
	// existence. It creates an independent identity in this caller's scope.
	otherSum := sha256.Sum256([]byte("other bytes"))
	otherStart := strings.Replace(strings.Replace(start, "field.txt", "other.txt", 1), hex.EncodeToString(sum[:]), hex.EncodeToString(otherSum[:]), 1)
	call("POST", "/file-transfers", otherStart, 201)
	call("PUT", "/file-transfers/"+id+"/chunks?offset=0", "other bytes", 200)
	call("POST", "/file-transfers/"+id+"/verify", "", 200)
	sameUserDevice, err := sessions.CreateMobile(ctx, u.ID, "Second own device")
	if err != nil {
		t.Fatal(err)
	}
	bearer.Data.Token = sameUserDevice
	call("GET", "/file-transfers/"+id, "", 404)
	call("POST", "/file-transfers/lookup", lookup, 404)
	call("POST", "/file-transfers", strings.Replace(start, "field.txt", "device.txt", 1), 201)
	bearer.Data.Token = old
	if original := call("POST", "/file-transfers/"+id+"/verify", "", 200); !strings.Contains(original, `"name":"field.txt"`) {
		t.Fatal("cross-caller UUID replaced original transfer")
	}
	// The same user on another device and another parent are not digest scopes.
	j2 := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Other parent").SaveX(ctx)
	call("POST", "/file-transfers/lookup", strings.Replace(lookup, fmt.Sprintf(`"object_id":%d`, j.ID), fmt.Sprintf(`"object_id":%d`, j2.ID), 1), 404)
	call("PUT", "/file-transfers/"+id+"/chunks?offset=0", "HELLO ", 409)
	call("PUT", "/file-transfers/"+id+"/chunks?offset=7", "world", 400)
	call("PUT", "/file-transfers/"+id+"/chunks?offset=0", strings.Repeat("x", (1<<20)+1), 413)
	badID := uuid.NewString()
	bad := strings.Replace(start, id, badID, 1)
	call("POST", "/file-transfers", bad, 201)
	call("PUT", "/file-transfers/"+badID+"/chunks?offset=0", "HELLO WORLD", 200)
	call("POST", "/file-transfers/"+badID+"/verify", "", 400)
	// Lowered current bounds apply to previously accepted transport identities.
	api = apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewEntDirectory(client), files.UploadDir(), 10))
	call("POST", "/file-transfers/"+id+"/verify", "", 400)
	api = apiv1.NewRouter(pool, client, sessions, files)
	// Concurrent identical retries have one byte effect and no new audit.
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("PUT", "/file-transfers/"+id+"/chunks?offset=6", strings.NewReader("world"))
			r.Header.Set("Operation-ID", uuid.NewString())
			r.Header.Set("Authorization", "Bearer "+bearer.Data.Token)
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent retry=%d", code)
		}
	}
	// Missing durable chunks never verify, even with a complete progress counter.
	if _, err := pool.Exec(ctx, `INSERT INTO file_transfer_chunks(transfer_id,offset_bytes,data,user_id,session_hash) SELECT id,5,$2,user_id,session_hash FROM file_transfers WHERE id=$1 AND name='field.txt'`, id, []byte("x")); err != nil {
		t.Fatal(err)
	}
	call("POST", "/file-transfers/"+id+"/verify", "", 500)
	if _, err := pool.Exec(ctx, `DELETE FROM file_transfer_chunks WHERE transfer_id=$1 AND offset_bytes=5`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM file_transfer_chunks WHERE transfer_id=$1 AND offset_bytes=6`, id); err != nil {
		t.Fatal(err)
	}
	call("POST", "/file-transfers/"+id+"/verify", "", 500)
	if _, err := pool.Exec(ctx, `INSERT INTO file_transfer_chunks(transfer_id,offset_bytes,data,user_id,session_hash) SELECT id,6,$2,user_id,session_hash FROM file_transfers WHERE id=$1 AND name='field.txt'`, id, []byte("world")); err != nil {
		t.Fatal(err)
	}
	// An audit storage failure rolls back the transfer start as well.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_transfer_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.detail ? 'transfer_id' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_transfer_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_transfer_audit()`); err != nil {
		t.Fatal(err)
	}
	failedID := uuid.NewString()
	call("POST", "/file-transfers", strings.Replace(start, id, failedID, 1), 500)
	call("GET", "/file-transfers/"+failedID, "", 404)
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_transfer_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	// A revocation wins while verification waits at the shared authority fence.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var blocker int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) boundary`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", "/file-transfers/"+id+"/verify", nil)
		r.Header.Set("Authorization", "Bearer "+bearer.Data.Token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		result <- w.Code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("verification did not reach fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='file.upload'`, role); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-result; code != 403 {
		t.Fatalf("revoked verification=%d", code)
	}
	call("GET", "/file-transfers/"+id, "", 403)
	if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.view'`, role); err != nil {
		t.Fatal(err)
	}
	call("GET", "/file-transfers/"+id, "", 404)
	call("POST", "/file-transfers/lookup", lookup, 404)
}

func TestTransferRestartProbe(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TRANSFER_PROBE_DSN")
	if dsn == "" {
		t.Skip("subprocess helper; exercised by TestTransferVerifiedWithoutPublicationHTTP")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, raw)))
	defer client.Close()
	api := apiv1.NewRouter(pool, client, services.NewSessionService(pool), services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 11))
	id := os.Getenv("FREEFSM_TRANSFER_PROBE_ID")
	for _, step := range []struct{ method, path, body string }{{"GET", "/file-transfers/" + id, ""}, {"PUT", "/file-transfers/" + id + "/chunks?offset=6", "world"}, {"POST", "/file-transfers/" + id + "/verify", ""}} {
		r := httptest.NewRequest(step.method, step.path, strings.NewReader(step.body))
		r.Header.Set("Authorization", "Bearer "+os.Getenv("FREEFSM_TRANSFER_PROBE_TOKEN"))
		stageTestFileTransport(r, step.body)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("restart %s: %d %s", step.method, w.Code, w.Body.String())
		}
	}
}

func TestTransferDownloadRejectsCorruptionHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Download customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Download work").SaveX(ctx)
	files := services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 11)
	f, err := files.Create(ctx, company, objectref.Ref{Type: objectref.TypeJob, ID: j.ID}, "field.txt", "", 11, strings.NewReader("hello world"), admin)
	if err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Download device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions, files)
	get := func(status int, ranges ...string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", fmt.Sprintf("/files/%d", f.ID), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Range", "bytes=6-10")
		if len(ranges) > 0 {
			r.Header.Set("Range", ranges[0])
		}
		if len(ranges) > 1 {
			r.Header.Set("If-Range", ranges[1])
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("download: %d want %d: %s", w.Code, status, w.Body.String())
		}
		return w
	}
	// Reading an authoritative File must not write transient snapshots into the
	// durable upload inventory (which the backup engine walks concurrently).
	if err := os.Chmod(files.UploadDir(), 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(files.UploadDir(), 0700) })
	get(206)
	if err := os.Chmod(files.UploadDir(), 0700); err != nil {
		t.Fatal(err)
	}
	first := get(206, "bytes=0-5")
	api = apiv1.NewRouter(pool, client, sessions, files)
	w := get(206, "bytes=6-10", first.Header().Get("ETag"))
	if first.Body.String()+w.Body.String() != "hello world" || w.Header().Get("ETag") != `"sha256-b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"` || w.Header().Get("Repr-Digest") != "sha-256=:uU0nuZNNPgilLlLX2n2r+sSE7+N6U4DukIj3rOLvzek=:" {
		t.Fatalf("resumed exact-byte representation mismatch: %v", w.Header())
	}
	if replaced := get(200, "bytes=6-10", `"different-representation"`); replaced.Body.String() != "hello world" {
		t.Fatal("If-Range mismatch returned bytes to append to stale content")
	}
	if w.Body.String() != "world" || w.Header().Get("Repr-Digest") == "" || w.Header().Get("ETag") == "" {
		t.Fatalf("unverified range: %v %s", w.Header(), w.Body.String())
	}
	if bad := get(416, "bytes=100-200"); !strings.Contains(bad.Body.String(), `"code":"invalid_range"`) {
		t.Fatalf("untyped range error: %s", bad.Body.String())
	}
	if err := os.WriteFile(f.FilePath, []byte("HELLO WORLD"), 0600); err != nil {
		t.Fatal(err)
	}
	get(500)
	if err := os.WriteFile(f.FilePath, []byte("hello world"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE files SET sha256=NULL WHERE id=$1`, f.ID); err != nil {
		t.Fatal(err)
	}
	get(206)
	if installed := client.File.GetX(ctx, f.ID); installed.Sha256 != nil || installed.UploadedBy != admin || installed.OriginalName != "field.txt" {
		t.Fatal("download invented historical integrity or replaced installed identity")
	}
	if err := os.Remove(f.FilePath); err != nil {
		t.Fatal(err)
	}
	get(500)
	a := authorization.New(pool)
	role, err := a.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "No download", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("download-denied@test").SetPasswordHash("hash").SetName("Denied").SetRole("admin").SaveX(ctx)
	if err := a.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	token, err = sessions.CreateMobile(ctx, u.ID, "Denied device")
	if err != nil {
		t.Fatal(err)
	}
	get(403)
	if _, err := a.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Still no download", nil, 1); err != nil {
		t.Fatal(err)
	}
	get(404)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET revoked_at=now() WHERE user_id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	get(401)
}

func TestTransferDefaultLimitHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Boundary").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Boundary").SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions, services.NewFileService(client, objectref.NewEntDirectory(client), t.TempDir(), 0))
	token, err := sessions.CreateMobile(ctx, admin, "Boundary device")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, status int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		stageTestFileTransport(r, body)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	if body := request("GET", "/files/limits", "", 200); !strings.Contains(body, `"max_file_bytes":26214400`) {
		t.Fatal(body)
	}
	chunk := strings.Repeat("x", 1<<20)
	h := sha256.New()
	for range 25 {
		_, _ = h.Write([]byte(chunk))
	}
	id := uuid.NewString()
	start := fmt.Sprintf(`{"id":%q,"publication_id":%q,"object_type":"job","object_id":%d,"name":"boundary.txt","size":26214400,"sha256":%q}`, id, uuid.NewString(), j.ID, hex.EncodeToString(h.Sum(nil)))
	request("POST", "/file-transfers", strings.Replace(start, "26214400", "26214401", 1), 400)
	request("POST", "/file-transfers", start, 201)
	for i := range 25 {
		request("PUT", fmt.Sprintf("/file-transfers/%s/chunks?offset=%d", id, i*(1<<20)), chunk, 200)
	}
	request("POST", "/file-transfers/"+id+"/verify", "", 200)
	if client.File.Query().CountX(ctx) != 0 {
		t.Fatal("limit test published attachments")
	}
	token = ""
	request("GET", "/files/limits", "", 401)
	request("GET", "/file-transfers/"+id, "", 401)
}
