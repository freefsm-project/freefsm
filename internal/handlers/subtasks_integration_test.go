package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestSubtaskIdentityAndIndependentCompletionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Checklist owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Checklist work").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	masterPost(t, router, cookie, path, url.Values{"action": {"add"}, "title": {"Inspect pump"}, "operation_id": {uuid.NewString()}}, 303)
	var tasks []struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Completed bool   `json:"completed"`
		Revision  string `json:"completion_revision"`
	}
	if err := json.Unmarshal([]byte(client.Job.GetX(ctx, j.ID).Subtasks), &tasks); err != nil || len(tasks) != 1 || tasks[0].ID == "" || tasks[0].Revision == "" {
		t.Fatalf("stable checklist projection: %+v, %v", tasks, err)
	}
	id, revision := tasks[0].ID, tasks[0].Revision
	masterPost(t, router, cookie, path, url.Values{"action": {"rename"}, "subtask_id": {id}, "title": {"Inspect replacement pump"}, "operation_id": {uuid.NewString()}}, 303)
	masterPost(t, router, cookie, path, url.Values{"action": {"add"}, "title": {"Unrelated valve"}, "operation_id": {uuid.NewString()}}, 303)
	if err := json.Unmarshal([]byte(client.Job.GetX(ctx, j.ID).Subtasks), &tasks); err != nil || len(tasks) != 2 {
		t.Fatalf("second entry: %+v %v", tasks, err)
	}
	masterPost(t, router, cookie, path, url.Values{"action": {"reorder"}, "order": {tasks[1].ID, id}, "operation_id": {uuid.NewString()}}, 303)
	masterPost(t, router, cookie, path, url.Values{"action": {"complete"}, "subtask_id": {id}, "completion_revision": {revision}, "operation_id": {uuid.NewString()}}, 303)
	if err := json.Unmarshal([]byte(client.Job.GetX(ctx, j.ID).Subtasks), &tasks); err != nil || len(tasks) != 2 || tasks[1].ID != id || !tasks[1].Completed || tasks[1].Revision == revision || tasks[1].Title != "Inspect replacement pump" || tasks[0].Completed {
		t.Fatalf("completion lost independent rename or identity: %+v, %v", tasks, err)
	}
	if client.Job.GetX(ctx, j.ID).StatusID != j.StatusID {
		// This fixture deliberately has no Status; checklist actions cannot select one.
		t.Fatal("completion changed Job Status")
	}
}

func TestSubtaskLegacyUpgradeRequiresReviewedMappingHTTP(t *testing.T) {
	ctx := context.Background()
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		// Install the historical schema immediately before Subtask identity/review.
		if entry.IsDir() || entry.Name() >= "076_subtask_identity.up.sql" {
			continue
		}
		b, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: b}
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed checklist owner").SaveX(ctx)
	raw := `[{"title":"Recorded completed row","completed":true,"sort_order":7},{"title":"Recorded incomplete row","completed":false,"sort_order":9}]`
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Installed work").SetSubtasks(raw).SaveX(ctx)
	if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	masterPost(t, router, cookie, path, url.Values{"action": {"add"}, "title": {"Must not overwrite"}, "operation_id": {uuid.NewString()}}, 409)
	var unchanged bool
	if err := pool.QueryRow(ctx, `SELECT subtasks=$2::jsonb FROM jobs WHERE id=$1`, j.ID, raw).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("migration/rejected mutation altered evidence: %v", err)
	}
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Unknown completion actors and times remain unknown")
	review, err := authorization.New(pool).SubtaskReview(ctx, authorization.Actor{UserID: admin}, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, path+"/review", url.Values{"snapshot": {"stale"}, "acknowledge": {"true"}}, 409)
	masterPost(t, router, cookie, path+"/review", url.Values{"snapshot": {review.Snapshot}, "acknowledge": {"true"}}, 303)
	check, err := authorization.New(pool).Subtasks(ctx, authorization.Actor{UserID: admin}, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if check.ReviewRequired || len(check.Entries) != 2 || len(check.History) != 0 || check.Entries[0].Title != "Recorded completed row" || !check.Entries[0].Completed || check.Entries[0].SortOrder != 7 || check.Entries[0].CompletionActor != nil || check.Entries[0].CompletionAt != nil {
		t.Fatalf("invented or lost facts: %+v", check)
	}
	var retained bool
	if err := pool.QueryRow(ctx, `SELECT recorded=$2::jsonb AND reviewed_by=$3 AND jsonb_array_length(mapping)=2 FROM subtask_legacy_reviews WHERE job_id=$1`, j.ID, raw, admin).Scan(&retained); err != nil || !retained {
		t.Fatalf("mapping/evidence missing: %v", err)
	}
	masterPost(t, router, cookie, path, url.Values{"action": {"add"}, "title": {"Appended after recorded order"}, "operation_id": {uuid.NewString()}}, 303)
	check, err = authorization.New(pool).Subtasks(ctx, authorization.Actor{UserID: admin}, j.ID)
	if err != nil || len(check.Entries) != 3 || check.Entries[2].Title != "Appended after recorded order" || check.Entries[0].SortOrder != 7 || check.Entries[1].SortOrder != 9 {
		t.Fatalf("new row displaced recorded order: %+v %v", check, err)
	}
}

func TestSubtaskAmbiguousLegacyReferenceRemainsNonDestructivelyBlockedHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	raw := `[{"title":"Recorded external reference","completed":true,"sort_order":1,"external_reference":"unresolved-row-identity"}]`
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Imported legacy evidence").SetSubtasks(raw).SaveX(ctx)
	review, err := auth.SubtaskReview(ctx, actor, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	masterPost(t, router, cookie, path+"/review", url.Values{"snapshot": {review.Snapshot}, "acknowledge": {"true"}}, 409)
	masterPost(t, router, cookie, path, url.Values{"action": {"add"}, "title": {"Must not overwrite evidence"}, "operation_id": {uuid.NewString()}}, 409)
	var retained bool
	if err := pool.QueryRow(ctx, `SELECT j.subtasks=$2::jsonb AND r.recorded=$2::jsonb AND r.reviewed_at IS NULL AND NOT EXISTS(SELECT 1 FROM job_subtasks WHERE job_id=j.id) FROM jobs j JOIN subtask_legacy_reviews r ON r.job_id=j.id WHERE j.id=$1`, j.ID, raw).Scan(&retained); err != nil || !retained {
		t.Fatalf("ambiguous evidence altered or cutover silently accepted: %v", err)
	}
}

func subtaskAPI(t *testing.T, router http.Handler, token, method, path string, in any, status int) []byte {
	t.Helper()
	b, _ := json.Marshal(in)
	r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return w.Body.Bytes()
}

func TestSubtaskConcurrentSameOperationHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Duplicate intent").SaveX(ctx)
	if _, err := auth.SubmitSubtask(ctx, actor, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "add", Title: "One effect"}); err != nil {
		t.Fatal(err)
	}
	check, err := auth.Subtasks(ctx, actor, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Concurrent checklist client")
	if err != nil {
		t.Fatal(err)
	}
	router := chi.NewRouter()
	router.Mount("/api/v1", apiv1.NewRouter(pool, client, sessions))
	path := fmt.Sprintf("/api/v1/jobs/%d/subtasks/%s", j.ID, check.Entries[0].ID)
	body, _ := json.Marshal(map[string]any{"operation_id": uuid.NewString(), "completion_revision": check.Entries[0].CompletionRevision, "completed": true})
	type response struct {
		code int
		body string
	}
	done := make(chan response, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			r := httptest.NewRequest("PATCH", path, strings.NewReader(string(body)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			done <- response{w.Code, w.Body.String()}
		}()
	}
	close(start)
	a, b := <-done, <-done
	if a.code != 200 || b.code != 200 || a.body != b.body {
		t.Fatalf("concurrent replay diverged: %+v %+v", a, b)
	}
	check, err = auth.Subtasks(ctx, actor, j.ID)
	if err != nil || len(check.History) != 2 || !check.Entries[0].Completed {
		t.Fatalf("duplicate/lost effect: %+v %v", check, err)
	}
}

func TestSubtaskAPIRevisionReceiptsAndScopeLossHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	router := chi.NewRouter()
	router.Mount("/api/v1", apiv1.NewRouter(pool, client, sessions))
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Private checklist").SaveX(ctx)
	added, err := auth.SubmitSubtask(ctx, actor, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "add", Title: "Secret pump"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := auth.SaveRole(ctx, actor, 0, "Exact field grants", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.subtask.complete", Scope: authorization.Assigned}, {Permission: "job.subtask.reopen", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Field actor").SetEmail("subtasks40@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, u.ID, "Exact checklist client")
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	var data struct {
		Data authorization.Checklist `json:"data"`
	}
	if err := json.Unmarshal(subtaskAPI(t, router, token, "GET", path, nil, 200), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Data.Entries) != 1 {
		t.Fatalf("checklist: %+v", data)
	}
	original := data.Data.Entries[0].CompletionRevision
	command := map[string]any{"operation_id": uuid.NewString(), "completed": true, "completion_revision": original, "occurred_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)}
	mutationPath := path + "/" + added.SubtaskID
	subtaskAPI(t, router, token, "POST", path, map[string]any{"title": "Forbidden planning"}, 405)
	subtaskAPI(t, router, token, "PATCH", mutationPath, map[string]any{"operation_id": uuid.NewString(), "completed": true, "completion_revision": original, "title": "Forbidden planning"}, 400)
	subtaskAPI(t, router, token, "PATCH", path+"/0", command, 404)
	command["operation_id"] = uuid.NewString()
	receipt := subtaskAPI(t, router, token, "PATCH", mutationPath, command, 200)
	var attributable bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE actor_id=$1 AND role_id=$2 AND action='job.subtask.complete' AND scope='J' AND channel='api' AND detail->>'operation_id'=$3 AND detail->>'device_name'='Exact checklist client' AND detail->>'occurred_at'=$4 AND recorded_at IS NOT NULL)`, u.ID, role, command["operation_id"], command["occurred_at"]).Scan(&attributable); err != nil || !attributable {
		t.Fatalf("missing device/operation/occurrence audit attribution: %v", err)
	}
	if replay := subtaskAPI(t, router, token, "PATCH", mutationPath, command, 200); string(replay) != string(receipt) {
		t.Fatal("same-intent receipt changed")
	}
	command["completed"] = false
	subtaskAPI(t, router, token, "PATCH", mutationPath, command, 409)
	// A new same-state intent preserves the prior actor/time/revision/history.
	beforeNoop, err := auth.Subtasks(ctx, actor, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	command["operation_id"] = uuid.NewString()
	command["completed"] = true
	subtaskAPI(t, router, token, "PATCH", mutationPath, command, 200)
	if _, err := auth.SubmitSubtask(ctx, actor, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "complete", SubtaskID: added.SubtaskID, CompletionRevision: original}); err != nil {
		t.Fatal(err)
	}
	check, err := auth.Subtasks(ctx, actor, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(check.History) != 2 || check.Entries[0].CompletionActor == nil || *check.Entries[0].CompletionActor != u.ID {
		t.Fatalf("no-op changed attribution: %+v", check)
	}
	if check.Entries[0].CompletionRevision != beforeNoop.Entries[0].CompletionRevision || check.Entries[0].CompletionAt == nil || beforeNoop.Entries[0].CompletionAt == nil || !check.Entries[0].CompletionAt.Equal(*beforeNoop.Entries[0].CompletionAt) {
		t.Fatal("same-state intent changed completion revision/time")
	}
	current := check.Entries[0].CompletionRevision
	command["operation_id"] = uuid.NewString()
	command["completed"] = false
	subtaskAPI(t, router, token, "PATCH", mutationPath, command, 409)
	command["operation_id"] = uuid.NewString()
	command["completion_revision"] = current
	subtaskAPI(t, router, token, "PATCH", mutationPath, command, 200)
	command["operation_id"] = uuid.NewString()
	command["completed"] = true
	command["completion_revision"] = original
	subtaskAPI(t, router, token, "PATCH", mutationPath, command, 409) // ABA
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	subtaskAPI(t, router, token, "GET", path, nil, 404)
	subtaskAPI(t, router, token, "PATCH", mutationPath, command, 409) // existing terminal rejection
	command["operation_id"] = uuid.NewString()
	subtaskAPI(t, router, token, "PATCH", mutationPath, command, 404)
	var result struct {
		Data authorization.SubtaskReceipt `json:"data"`
	}
	if err := json.Unmarshal(receipt, &result); err != nil {
		t.Fatal(err)
	}
	lookup := subtaskAPI(t, router, token, "GET", "/subtask-operations/"+result.Data.OperationID, nil, 200)
	if strings.Contains(string(lookup), "Secret") || !strings.Contains(string(lookup), added.SubtaskID) {
		t.Fatalf("receipt leaked text or lost stable effect: %s", lookup)
	}
	otherToken, err := sessions.CreateMobile(ctx, u.ID, "Different client")
	if err != nil {
		t.Fatal(err)
	}
	subtaskAPI(t, router, otherToken, "GET", "/subtask-operations/"+result.Data.OperationID, nil, 404)
}
