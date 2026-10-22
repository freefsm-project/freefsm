package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/google/uuid"
)

func TestOfflineFilesPublicationFailureAndMixedDependenciesHTTP(t *testing.T) {
	f := offlineFiles(t)
	ctx := context.Background()
	transfer, pub := f.transfer(t, "atomic.txt", "atomic bytes")
	op := fileBatchOp(pub, "file.publish", transfer, f.job)
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION fail_file_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected File audit failure'; END $$; CREATE TRIGGER fail_file_audit BEFORE INSERT ON authorization_audit FOR EACH ROW WHEN (NEW.target_type='file') EXECUTE FUNCTION fail_file_audit()`); err != nil {
		t.Fatal(err)
	}
	out := f.batch(t, op)[0].(map[string]any)
	if out["receipt"] != nil || out["error"].(map[string]any)["code"] != "retryable_failure" {
		t.Fatalf("audit fault terminal/partial: %v", out)
	}
	f.call(t, "GET", "/file-operations/"+pub, nil, 404)
	if len(syncGet(t, f.api, f.token, "", 200).Records[0]["files"].([]any)) != 0 {
		t.Fatal("audit failure published")
	}
	if _, err := f.pool.Exec(ctx, `DROP TRIGGER fail_file_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if f.batch(t, op)[0].(map[string]any)["receipt"].(map[string]any)["code"] != "accepted" {
		t.Fatal("same-ID retry failed")
	}
	unknown := fileBatchOp(uuid.NewString(), "file.publish", transfer, f.job)
	unknown["depends_on"] = []string{uuid.NewString()}
	notes := f.call(t, "GET", fmt.Sprintf("/jobs/%d/field-notes", f.job), nil, 200)
	child := map[string]any{"operation_id": uuid.NewString(), "type": "job.field_notes", "target": fmt.Sprint(f.job), "depends_on": []string{unknown["operation_id"].(string)}, "notes_revision": notes["notes_revision"], "field_notes": "blocked"}
	good := map[string]any{"operation_id": uuid.NewString(), "type": "job.field_notes", "target": fmt.Sprint(f.job), "depends_on": []string{pub}, "notes_revision": notes["notes_revision"], "field_notes": "independent accepted"}
	for i, r := range f.batch(t, unknown, child, good) {
		if r.(map[string]any)["receipt"].(map[string]any)["code"] != []string{"invalid_dependency", "failed_dependency", "accepted"}[i] {
			t.Fatalf("mixed dependency%d: %v", i, r)
		}
	}
	changed := fileBatchOp(pub, "file.publish", uuid.NewString(), f.job)
	misuse := f.batch(t, changed)[0].(map[string]any)
	if misuse["receipt"] != nil || misuse["error"].(map[string]any)["status"] != float64(409) || misuse["error"].(map[string]any)["code"] != "operation_intent_changed" {
		t.Fatalf("changed intent borrowed success: %v", misuse)
	}
	badTransfer, badOp := uuid.NewString(), uuid.NewString()
	f.call(t, "POST", "/file-transfers", map[string]any{"id": badTransfer, "publication_id": badOp, "object_type": "job", "object_id": f.job, "name": "bad.txt", "size": 4, "sha256": strings.Repeat("a", 64)}, 201)
	f.call(t, "PUT", "/file-transfers/"+badTransfer+"/chunks?offset=0", "data", 200)
	bad := f.batch(t, fileBatchOp(badOp, "file.publish", badTransfer, f.job))[0].(map[string]any)
	if bad["receipt"].(map[string]any)["code"] != "invalid_file" {
		t.Fatalf("digest accepted: %v", bad)
	}
	var files, events int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM files),(SELECT count(*) FROM activity_logs WHERE action='file_uploaded')`).Scan(&files, &events); err != nil {
		t.Fatal(err)
	}
	if files != 1 || events != 1 {
		t.Fatalf("failure created partial effect/history: files%d events%d", files, events)
	}
}

func TestOfflineFilesOriginalParentNeverRetargetsHTTP(t *testing.T) {
	f := offlineFiles(t)
	ctx := context.Background()
	transfer, pub := f.transfer(t, "original-parent.txt", "original bytes")
	j := f.client.Job.GetX(ctx, f.job)
	other := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(j.CustomerID).SetJobType("Other authorized parent").SaveX(ctx)
	f.client.JobAssignment.Create().SetJobID(other.ID).SetUserID(f.user).SaveX(ctx)
	wrong := fileBatchOp(pub, "file.publish", transfer, other.ID)
	if r := f.batch(t, wrong)[0].(map[string]any)["receipt"].(map[string]any); r["code"] != "file_conflict" {
		t.Fatalf("retargeted publication: %v", r)
	}
	wrong["object_id"] = f.job
	if r := f.batch(t, wrong)[0].(map[string]any); r["receipt"] != nil {
		t.Fatalf("same-ID repair reinterpreted original parent: %v", r)
	}
	wrong["operation_id"] = uuid.NewString()
	r := f.batch(t, wrong)[0].(map[string]any)["receipt"].(map[string]any)
	id := fmt.Sprint(r["file_id"])
	meta := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	if _, err := f.pool.Exec(ctx, `UPDATE files SET object_id=$1,revision=revision+1 WHERE id=$2`, other.ID, id); err != nil {
		t.Fatal(err)
	}
	rename := fileBatchOp(uuid.NewString(), "file.rename", id, f.job)
	rename["revision"] = meta["revision"]
	rename["name"] = "not-moved.txt"
	if r := f.batch(t, rename)[0].(map[string]any)["receipt"].(map[string]any); r["code"] != "file_conflict" {
		t.Fatalf("followed moved File: %v", r)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, f.job); err != nil {
		t.Fatal(err)
	}
	archive := fileBatchOp(uuid.NewString(), "file.publish", transfer, f.job)
	if r := f.batch(t, archive)[0].(map[string]any)["receipt"].(map[string]any); r["code"] != "not_found" {
		t.Fatalf("archived original published: %v", r)
	}
}

func TestOfflineFilesActualMutationRacesHTTP(t *testing.T) {
	for _, first := range []string{"rename", "remove"} {
		t.Run(first, func(t *testing.T) {
			f := offlineFiles(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			transfer, pub := f.transfer(t, "race.txt", "race bytes")
			r := f.batch(t, fileBatchOp(pub, "file.publish", transfer, f.job))[0].(map[string]any)["receipt"].(map[string]any)
			id := fmt.Sprint(r["file_id"])
			meta := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
			if _, err := f.pool.Exec(ctx, `CREATE FUNCTION hold_file_mutation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(860086); RETURN NEW; END $$; CREATE TRIGGER hold_file_mutation BEFORE UPDATE OF original_name,removed_at ON files FOR EACH ROW EXECUTE FUNCTION hold_file_mutation()`); err != nil {
				t.Fatal(err)
			}
			barrier, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.Background())
			var blocker int32
			if err := barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(860086)) barrier`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			call := func(kind string, done chan *httptest.ResponseRecorder) {
				op := fileBatchOp(uuid.NewString(), "file."+kind, id, f.job)
				op["revision"] = meta["revision"]
				if kind == "rename" {
					op["name"] = "winner.txt"
				}
				raw, _ := json.Marshal(map[string]any{"operations": []any{op}})
				done <- batchRequest(f.api, f.token, string(raw))
			}
			one, two := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
			go call(first, one)
			var writer int32
			application := f.pool.Config().ConnConfig.RuntimeParams["application_name"]
			for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
				var candidates []int32
				err = f.pool.QueryRow(ctx, `SELECT coalesce(array_agg(a.pid),'{}') FROM pg_stat_activity a WHERE a.datname=current_database() AND a.application_name=$2 AND $1=ANY(pg_blocking_pids(a.pid)) AND a.wait_event='advisory' AND a.query LIKE 'UPDATE files SET %' AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.classid=0 AND l.objid=190019 AND l.granted)`, blocker, application).Scan(&candidates)
				if err != nil {
					t.Fatal(err)
				}
				if len(candidates) == 1 {
					writer = candidates[0]
					break
				}
				if len(candidates) > 1 {
					t.Fatal("ambiguous domain writer")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if writer == 0 {
				t.Fatal("actual File UPDATE never reached the fixture barrier")
			}
			assertForeignAuthorityExcluded(t, ctx, f.pool, writer)
			second := "remove"
			if first == "remove" {
				second = "rename"
			}
			go call(second, two)
			contender := waitAuthorityRequest(t, ctx, f.pool, writer, authorityWriterStatement)
			t.Logf("actual File %s UPDATE backend%d holds authority at fixture barrier%d; %s backend%d waits", first, writer, blocker, second, contender)
			if err := barrier.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			for i, w := range []*httptest.ResponseRecorder{<-one, <-two} {
				if w.Code != 200 {
					t.Fatalf("race status%d: %s", w.Code, w.Body.String())
				}
				var out struct {
					Data struct {
						Results []struct{ Receipt struct{ Code string } }
					}
				}
				if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
					t.Fatal(err)
				}
				want := "accepted"
				if i == 1 {
					want = "file_conflict"
					if first == "remove" {
						want = "not_found"
					}
				}
				if out.Data.Results[0].Receipt.Code != want {
					t.Fatalf("race%d want%s: %s", i, want, w.Body.String())
				}
			}
			meta = f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
			if first == "rename" && (meta["name"] != "winner.txt" || meta["removed_at"] != nil) || first == "remove" && (meta["name"] != "race.txt" || meta["removed_at"] == nil) {
				t.Fatalf("incompatible persisted race: %v", meta)
			}
		})
	}
}

func TestOfflineFilesAuthorityLossBeforeCommitHTTP(t *testing.T) {
	f := offlineFiles(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	transfer, pub := f.transfer(t, "authority.txt", "protected bytes")
	r := f.batch(t, fileBatchOp(pub, "file.publish", transfer, f.job))[0].(map[string]any)["receipt"].(map[string]any)
	id := fmt.Sprint(r["file_id"])
	meta := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	op := fileBatchOp(uuid.NewString(), "file.rename", id, f.job)
	op["revision"] = meta["revision"]
	op["name"] = "denied.txt"
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blocker int32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) held`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	assertForeignAuthorityExcluded(t, ctx, f.pool, blocker)
	done := make(chan *httptest.ResponseRecorder, 1)
	raw, _ := json.Marshal(map[string]any{"operations": []any{op}})
	go func() { done <- batchRequest(f.api, f.token, string(raw)) }()
	waitAuthorityRequest(t, ctx, f.pool, blocker, authorityWriterStatement)
	if _, err := tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, f.user); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"not_found"`) {
		t.Fatalf("commit authority: %d %s", w.Code, w.Body.String())
	}
	f.call(t, "GET", "/files/"+id+"/metadata", nil, 404)
	safe := f.call(t, "GET", "/file-operations/"+op["operation_id"].(string), nil, 200)
	if safe["code"] != "not_found" || strings.Contains(fmt.Sprint(safe), "authority.txt") {
		t.Fatalf("unsafe rejection: %v", safe)
	}
}

func TestOfflineFilesDocumentParentsIndependentlyAuthorizedHTTP(t *testing.T) {
	f := offlineFiles(t)
	ctx := context.Background()
	job := f.client.Job.GetX(ctx, f.job)
	e := f.client.Estimate.Create().SetCompanyID(f.company).SetCustomerID(job.CustomerID).SetJobID(f.job).SetTitle("File Estimate").SaveX(ctx)
	i := f.client.Invoice.Create().SetCompanyID(f.company).SetCustomerID(job.CustomerID).SetJobID(f.job).SetTitle("File Invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	userToken := f.token
	adminToken, err := f.sessions.CreateMobile(ctx, f.admin, "Document Files")
	if err != nil {
		t.Fatal(err)
	}
	f.token = adminToken
	for kind, id := range map[string]int64{"estimate": e.ID, "invoice": i.ID} {
		tr, pub := f.transferAt(t, kind, id, kind+".txt", "document bytes")
		op := fileBatchOp(pub, "file.publish", tr, id)
		op["object_type"] = kind
		f.batch(t, op)
	}
	f.token = userToken
	page := syncGet(t, f.api, f.token, "", 200)
	if len(page.Records[0]["files"].([]any)) != 0 || len(page.Records[0]["file_parents"].([]any)) != 1 {
		t.Fatal("Job View expanded document metadata")
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'estimate.view','J'),($1,'invoice.view','J')`, f.role); err != nil {
		t.Fatal(err)
	}
	page = syncGet(t, f.api, f.token, "", 200)
	if len(page.Records[0]["files"].([]any)) != 2 || len(page.Records[0]["file_parents"].([]any)) != 3 {
		t.Fatalf("independent document scope: %v", page.Records[0])
	}
	for _, item := range page.Records[0]["files"].([]any) {
		caps := item.(map[string]any)["capabilities"].(map[string]any)
		if caps["rename"] != false || caps["remove"] != false || caps["download"] != true {
			t.Fatalf("document uploader capabilities: %v", caps)
		}
	}
	if _, err := conversion.New(f.pool).Convert(ctx, conversion.Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: e.ID}); err != nil {
		t.Fatal(err)
	}
	continuityResnapshot(t, continuityRequest(f.api, f.token, "?cursor="+*page.Cursor))
}

func TestOfflineFilesPublicationStorageRetryHTTP(t *testing.T) {
	f := offlineFiles(t)
	transfer, pub := f.transfer(t, "durable.txt", "durable bytes")
	op := fileBatchOp(pub, "file.publish", transfer, f.job)
	if err := os.Chmod(f.uploadDir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(f.uploadDir, 0700) })
	r := f.batch(t, op)[0].(map[string]any)
	if r["receipt"] != nil || r["error"].(map[string]any)["code"] != "retryable_failure" {
		t.Fatalf("storage failure falsely terminal: %v", r)
	}
	f.call(t, "GET", "/file-operations/"+pub, nil, 404)
	if err := os.Chmod(f.uploadDir, 0700); err != nil {
		t.Fatal(err)
	}
	if r := f.batch(t, op)[0].(map[string]any)["receipt"].(map[string]any); r["code"] != "accepted" {
		t.Fatalf("same-ID storage recovery: %v", r)
	}
}

func TestOfflineFilesConcurrentRejectionCannotBorrowReceiptHTTP(t *testing.T) {
	f := offlineFiles(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	transfer, pub := f.transfer(t, "existing.txt", "existing bytes")
	r := f.batch(t, fileBatchOp(pub, "file.publish", transfer, f.job))[0].(map[string]any)["receipt"].(map[string]any)
	id := fmt.Sprint(r["file_id"])
	meta := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	shared, incomplete := uuid.NewString(), uuid.NewString()
	f.call(t, "POST", "/file-transfers", map[string]any{"id": incomplete, "publication_id": shared, "object_type": "job", "object_id": f.job, "name": "incomplete.txt", "size": 4, "sha256": strings.Repeat("a", 64)}, 201)
	readGate, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer readGate.Rollback(context.Background())
	var blocker int32
	if _, err := readGate.Exec(ctx, `LOCK TABLE file_transfers IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	if err := readGate.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	request := fileBatchOp(shared, "file.publish", incomplete, f.job)
	raw, _ := json.Marshal(map[string]any{"operations": []any{request}})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- batchRequest(f.api, f.token, string(raw)) }()
	application := f.pool.Config().ConnConfig.RuntimeParams["application_name"]
	var reader int32
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var pids []int32
		if err := f.pool.QueryRow(ctx, `SELECT coalesce(array_agg(pid),'{}') FROM pg_stat_activity WHERE datname=current_database() AND application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock' AND query LIKE 'SELECT id::text,publication_id::text,%'`, blocker, application).Scan(&pids); err != nil {
			t.Fatal(err)
		}
		if len(pids) == 1 {
			reader = pids[0]
			break
		}
		if len(pids) > 1 {
			t.Fatal("ambiguous publication reader")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if reader == 0 {
		t.Fatal("publication did not pass initial admission lookup and reach scoped transfer read")
	}
	// While publication is paused AFTER its original receipt miss, the same
	// caller commits a different actual domain mutation under that UUID.
	f.call(t, "POST", "/files/"+id+"/rename", map[string]any{"operation_id": shared, "object_type": "job", "object_id": f.job, "revision": meta["revision"], "name": "accepted-mutation.txt"}, 200)
	current := f.call(t, "GET", "/files/"+id+"/metadata", nil, 200)
	if current["name"] != "accepted-mutation.txt" {
		t.Fatal("competing domain UPDATE did not commit")
	}
	t.Logf("fixture=%s publication backend%d blocked by transfer-table holder%d after receipt miss; actual File rename committed before read release", application, reader, blocker)
	if err := readGate.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != 200 {
		t.Fatalf("publication response: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data struct{ Results []map[string]any }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	result := out.Data.Results[0]
	if result["receipt"] != nil || result["error"].(map[string]any)["status"] != float64(409) || result["error"].(map[string]any)["code"] != "operation_intent_changed" {
		t.Fatalf("publication borrowed a different immutable receipt: %s", w.Body.String())
	}
	if receipt := f.call(t, "GET", "/file-operations/"+shared, nil, 200); receipt["effect_id"] != id || receipt["code"] != "accepted" {
		t.Fatalf("original mutation receipt changed: %v", receipt)
	}
}
