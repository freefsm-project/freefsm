package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func TestJobFieldSyncSnapshotAndSubtaskHistoryHTTP(t *testing.T) {
	f, api, token, _, _, ids := syncFixture(t, 1)
	ctx := context.Background()
	path := fmt.Sprintf("/jobs/%d/subtasks", ids[0])
	masterPost(t, f.router, f.cookie, path, url.Values{"action": {"add"}, "title": {"Original field task"}, "operation_id": {uuid.NewString()}}, 303)
	first := syncGet(t, api, token, "", 200)
	row := first.Records[0]
	for _, key := range []string{"status_revision", "status_definitions", "subtasks", "subtask_history", "field_capabilities"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("authorized snapshot missing %s: %v", key, row)
		}
	}
	tasks := row["subtasks"].([]any)
	if len(tasks) != 1 {
		t.Fatalf("checklist: %v", tasks)
	}
	task := tasks[0].(map[string]any)
	id, revision := task["id"].(string), task["completion_revision"].(string)
	if task["title"] != "Original field task" || revision == "" || task["completed"] != false {
		t.Fatalf("stable independent state: %v", task)
	}
	caps := row["field_capabilities"].(map[string]any)
	for _, key := range []string{"status", "subtask_complete", "subtask_reopen"} {
		if caps[key] != false {
			t.Fatalf("view-only reader gained %s: %v", key, caps)
		}
	}
	unchanged := syncGet(t, api, token, "?cursor="+*first.Cursor, 200)
	if len(unchanged.Records) != 0 {
		t.Fatalf("unchanged recorded facts produced false delta: %+v", unchanged)
	}
	first = unchanged
	masterPost(t, f.router, f.cookie, path, url.Values{"action": {"rename"}, "subtask_id": {id}, "title": {"Renamed field task"}, "operation_id": {uuid.NewString()}}, 303)
	delta := syncGet(t, api, token, "?cursor="+*first.Cursor, 200)
	if len(delta.Records) != 1 {
		t.Fatalf("rename missing from delta: %+v", delta)
	}
	renamed := delta.Records[0]["subtasks"].([]any)[0].(map[string]any)
	if renamed["id"] != id || renamed["completion_revision"] != revision || renamed["title"] != "Renamed field task" {
		t.Fatalf("rename changed completion identity: %v", renamed)
	}
	masterPost(t, f.router, f.cookie, path, url.Values{"action": {"remove"}, "subtask_id": {id}, "operation_id": {uuid.NewString()}}, 303)
	removed := syncGet(t, api, token, "?cursor="+*delta.Cursor, 200)
	if len(removed.Records) != 1 || len(removed.Records[0]["subtasks"].([]any)) != 0 || len(removed.Records[0]["subtask_history"].([]any)) != 3 {
		t.Fatalf("removal/history not synchronizable: %+v", removed)
	}
	var retained bool
	if err := f.pool.QueryRow(ctx, `SELECT removed_at IS NOT NULL AND completion_revision::text=$2 FROM job_subtasks WHERE id=$1`, id, revision).Scan(&retained); err != nil || !retained {
		t.Fatalf("removed identity lost: %v", err)
	}
}

func TestJobFieldSyncOrderedStatusAndSubtaskHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Field sync").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Independent field actions").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	masterPost(t, router, cookie, path, url.Values{"action": {"add"}, "title": {"Check"}, "operation_id": {uuid.NewString()}}, 303)
	token, err := sessions.CreateMobile(ctx, admin, "Field sync")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	page := syncGet(t, api, token, "", 200)
	row := page.Records[0]
	task := row["subtasks"].([]any)[0].(map[string]any)
	var completed map[string]any
	for _, value := range row["status_definitions"].([]any) {
		v := value.(map[string]any)
		if v["category"] == "job:completed" {
			completed = v
		}
	}
	if completed == nil {
		t.Fatal("missing Completed definition")
	}
	status := map[string]any{"operation_id": uuid.NewString(), "type": "job.status", "target": row["id"], "depends_on": []string{}, "status_id": completed["id"], "status_revision": row["status_revision"], "meaning_revision": completed["meaning_revision"]}
	finish := map[string]any{"operation_id": uuid.NewString(), "type": "job.subtask", "target": row["id"], "depends_on": []string{status["operation_id"].(string)}, "subtask_id": task["id"], "completion_revision": task["completion_revision"], "completed": true}
	submit := func(ops ...map[string]any) []authorization.JobExecutionReceipt {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"operations": ops})
		w := batchRequest(api, token, string(body))
		if w.Code != 200 {
			t.Fatalf("field batch %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Data struct {
				Results []struct {
					Receipt authorization.JobExecutionReceipt `json:"receipt"`
				} `json:"results"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		result := []authorization.JobExecutionReceipt{}
		for _, r := range out.Data.Results {
			result = append(result, r.Receipt)
		}
		return result
	}
	receipts := submit(status, finish)
	if len(receipts) != 2 || receipts[0].Code != "accepted" || receipts[1].Code != "accepted" {
		t.Fatalf("ordered independent actions: %+v", receipts)
	}
	var revision, attribution string
	if err := pool.QueryRow(ctx, `SELECT completion_revision::text,completion_actor::text||':'||completion_at::text FROM job_subtasks WHERE id=$1 AND completed`, task["id"]).Scan(&revision, &attribution); err != nil {
		t.Fatal(err)
	}
	// A newly reviewed same-state request may carry an old independent revision;
	// it must retain the first actor/time, even on a Completed active Job.
	finish["operation_id"] = uuid.NewString()
	if r := submit(finish); r[0].Code != "accepted" {
		t.Fatalf("same-state: %+v", r)
	}
	var got string
	if err := pool.QueryRow(ctx, `SELECT completion_actor::text||':'||completion_at::text FROM job_subtasks WHERE id=$1`, task["id"]).Scan(&got); err != nil || got != attribution {
		t.Fatalf("no-op changed attribution %q %v", got, err)
	}
	finish["operation_id"] = uuid.NewString()
	finish["completed"] = false
	if r := submit(finish); r[0].Code != "subtask_conflict" {
		t.Fatalf("stale changed state: %+v", r)
	}
	finish["operation_id"] = uuid.NewString()
	finish["completion_revision"] = revision
	if r := submit(finish); r[0].Code != "accepted" {
		t.Fatalf("reviewed reopen: %+v", r)
	}
	masterPost(t, router, cookie, path, url.Values{"action": {"remove"}, "subtask_id": {task["id"].(string)}, "operation_id": {uuid.NewString()}}, 303)
	finish["operation_id"] = uuid.NewString()
	finish["completed"] = true
	if r := submit(finish); r[0].Code != "not_found" {
		t.Fatalf("removed task resurrected: %+v", r)
	}
	if r := submit(status); r[0] != receipts[0] {
		t.Fatalf("status replay changed: %+v", r)
	}
}
