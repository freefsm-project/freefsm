package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/status"
	"github.com/google/uuid"
)

func TestSubtaskIndependentGrantsAndFinishedJobsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Checklist").SaveX(ctx)
	added, err := auth.SubmitSubtask(ctx, actor, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "add", Title: "Exact row"})
	if err != nil {
		t.Fatal(err)
	}
	role, err := auth.SaveRole(ctx, actor, 0, "Completion only", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.subtask.complete", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Completer").SetEmail("completer40@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	post := func(action string, code int) {
		check, err := auth.Subtasks(ctx, actor, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		masterPost(t, router, cookie, path, url.Values{"action": {action}, "title": {"Must not change"}, "subtask_id": {added.SubtaskID}, "completion_revision": {check.Entries[0].CompletionRevision}, "operation_id": {uuid.NewString()}}, code)
	}
	for _, action := range []string{"add", "rename", "remove", "reorder", "reopen"} {
		post(action, 403)
	}
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, ">Complete</button>")
	assertNotContains(t, body, ">Add Subtask</button>")
	for _, category := range []string{"job:completed", "job:canceled"} {
		st := client.Status.Query().Where(status.CompanyIDEQ(company), status.CategoryKeyEQ(category)).FirstX(ctx)
		masterPost(t, router, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/jobs/%d/status", j.ID), url.Values{"status_id": {fmt.Sprint(st.ID)}}, 303)
		post("complete", 303)
		if got := client.Job.GetX(ctx, j.ID); got.StatusID == nil || *got.StatusID != st.ID {
			t.Fatal("Subtask changed finished Job Status")
		}
		check, _ := auth.Subtasks(ctx, actor, j.ID)
		_, err := auth.SubmitSubtask(ctx, actor, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "reopen", SubtaskID: added.SubtaskID, CompletionRevision: check.Entries[0].CompletionRevision})
		if err != nil {
			t.Fatal(err)
		}
	}
	client.Job.UpdateOneID(j.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	post("complete", 404)
	body = requestBody(t, router, cookie, "GET", path, 404)
	assertNotContains(t, body, "Exact row")
}

func TestSubtaskReorderRemovalConcurrencyAndAuditRollbackHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Racing checklist").SaveX(ctx)
	for _, title := range []string{"First", "Second", "Third"} {
		if _, err := auth.SubmitSubtask(ctx, actor, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "add", Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	check, err := auth.Subtasks(ctx, actor, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, b, cid := check.Entries[0].ID, check.Entries[1].ID, check.Entries[2].ID
	foreign := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Other checklist").SaveX(ctx)
	foreignTask, err := auth.SubmitSubtask(ctx, actor, foreign.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "add", Title: "Foreign checklist row"})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	for _, invalid := range [][]string{{a, b}, {a, a, cid}, {a, b, foreignTask.SubtaskID}} {
		code := 400
		if len(invalid) != 3 {
			code = 409
		}
		masterPost(t, router, cookie, path, url.Values{"action": {"reorder"}, "order": invalid, "operation_id": {uuid.NewString()}}, code)
	}
	post := func(in url.Values) int {
		r := httptest.NewRequest("POST", path, strings.NewReader(in.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w.Code
	}
	start := make(chan struct{})
	done := make(chan int, 3)
	commands := []url.Values{
		{"action": {"remove"}, "subtask_id": {a}, "operation_id": {uuid.NewString()}},
		{"action": {"complete"}, "subtask_id": {a}, "completion_revision": {check.Entries[0].CompletionRevision}, "operation_id": {uuid.NewString()}},
		{"action": {"reorder"}, "order": {cid, b, a}, "operation_id": {uuid.NewString()}},
	}
	for _, command := range commands {
		go func(in url.Values) { <-start; done <- post(in) }(command)
	}
	close(start)
	for range commands {
		code := <-done
		if code != 303 && code != 404 && code != 409 {
			t.Fatalf("race response %d", code)
		}
	}
	check, err = auth.Subtasks(ctx, actor, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(check.Entries) != 2 {
		t.Fatalf("lost/resurrected entries %+v", check.Entries)
	}
	for _, v := range check.Entries {
		if v.ID != b && v.ID != cid {
			t.Fatal("removed entry resurrected")
		}
	}
	masterPost(t, router, cookie, path, commands[1], func() int {
		var code string
		if err := pool.QueryRow(ctx, `SELECT code FROM subtask_operation_receipts WHERE operation_id=$1`, commands[1].Get("operation_id")).Scan(&code); err != nil {
			t.Fatal(err)
		}
		if code == "accepted" {
			return 303
		}
		return 404
	}())
	// Reject audit insertion after the projection/history writes. The entire
	// command and receipt must roll back; retrying the SAME ID then succeeds.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_subtask_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='job.subtask.complete' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_subtask_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_subtask_audit()`); err != nil {
		t.Fatal(err)
	}
	command := url.Values{"action": {"complete"}, "subtask_id": {check.Entries[0].ID}, "completion_revision": {check.Entries[0].CompletionRevision}, "operation_id": {uuid.NewString()}}
	before := client.Job.GetX(ctx, j.ID).Subtasks
	masterPost(t, router, cookie, path, command, 500)
	if after := client.Job.GetX(ctx, j.ID).Subtasks; after != before {
		t.Fatal("audit failure changed projection")
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subtask_operation_receipts WHERE operation_id=$1)`, command.Get("operation_id")).Scan(&exists); err != nil || exists {
		t.Fatalf("transient failure became terminal: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_subtask_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, path, command, 303)
	if _, err := pool.Exec(ctx, `UPDATE subtask_history SET action='fabricated' WHERE job_id=$1`, j.ID); err == nil {
		t.Fatal("history mutable")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM subtask_operation_receipts WHERE operation_id=$1`, command.Get("operation_id")); err == nil {
		t.Fatal("receipt mutable")
	}
}

func TestSubtaskCommitTimeAuthorityHTTP(t *testing.T) {
	for _, race := range []string{"grant", "assignment", "archive", "session", "disabled"} {
		t.Run(race, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			auth := authorization.New(pool)
			actor := authorization.Actor{UserID: admin}
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Commit-time checklist").SaveX(ctx)
			_, err := auth.SubmitSubtask(ctx, actor, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "add", Title: "Must remain incomplete"})
			if err != nil {
				t.Fatal(err)
			}
			role, err := auth.SaveRole(ctx, actor, 0, "Revocable checklist", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.subtask.complete", Scope: authorization.Assigned}})
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetName("Revocable").SetEmail("revocable40@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
			if err := auth.AssignUser(ctx, actor, u.ID, role); err != nil {
				t.Fatal(err)
			}
			client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			check, _ := auth.Subtasks(ctx, actor, j.ID)
			in := url.Values{"action": {"complete"}, "subtask_id": {check.Entries[0].ID}, "completion_revision": {check.Entries[0].CompletionRevision}, "operation_id": {uuid.NewString()}}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var pid int
			if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) locked`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				r := httptest.NewRequest("POST", fmt.Sprintf("/jobs/%d/subtasks", j.ID), strings.NewReader(in.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w.Code
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				var waiting bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("request did not reach authority fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch race {
			case "grant":
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.subtask.complete'`, role)
			case "assignment":
				_, err = tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID)
			case "archive":
				_, err = tx.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, j.ID)
			case "session":
				_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, u.ID)
			case "disabled":
				_, err = tx.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, u.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			code := <-done
			if code != 403 && code != 404 {
				t.Fatalf("lost %s authority accepted: %d", race, code)
			}
			var completed bool
			if err := pool.QueryRow(ctx, `SELECT completed FROM job_subtasks WHERE id=$1`, check.Entries[0].ID).Scan(&completed); err != nil || completed {
				t.Fatalf("partial effect after lost authority: %v", err)
			}
		})
	}
}

func TestSubtaskPlanningCannotCompleteAndRejectsInvalidTitlesHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	for _, title := range []string{"", "\t\n\u2003"} {
		input, _ := json.Marshal([]map[string]any{{"title": title, "completed": false}})
		masterPost(t, router, cookie, "/jobs", url.Values{"form_mode": {"create"}, "job_id": {"0"}, "job_type": {"Invalid checklist"}, "customer_id": {fmt.Sprint(c.ID)}, "subtasks": {string(input)}}, 400)
	}
	if client.Job.Query().CountX(ctx) != 0 {
		t.Fatal("invalid checklist partially created Job")
	}
	masterPost(t, router, cookie, "/jobs", url.Values{"form_mode": {"create"}, "job_id": {"0"}, "job_type": {"Ordered checklist"}, "customer_id": {fmt.Sprint(c.ID)}, "subtasks": {`[{"title":"Second by explicit order","completed":false,"sort_order":20},{"title":"First by explicit order","completed":false,"sort_order":10}]`}}, 303)
	var rows []authorization.Subtask
	if err := json.Unmarshal([]byte(client.Job.Query().OnlyX(ctx).Subtasks), &rows); err != nil || len(rows) != 2 || rows[0].Title != "First by explicit order" {
		t.Fatalf("details planning ignored explicit order: %+v %v", rows, err)
	}
	j := client.Job.Query().OnlyX(ctx)
	rows[0].Completed = true
	input, _ := json.Marshal(rows)
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d", j.ID), url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "job_type": {j.JobType}, "customer_id": {fmt.Sprint(c.ID)}, "subtasks": {string(input)}}, 400)
}

func TestSubtaskMaintenanceNeedsNoGeneralEditAndCannotCompleteHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "job.subtask.manage", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "Checklist curator", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Curator").SetEmail("curator40@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unassigned curated Job").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	masterPost(t, router, cookie, path, url.Values{"action": {"add"}, "title": {"Curated row"}, "operation_id": {uuid.NewString()}}, 303)
	check, err := auth.Subtasks(ctx, actor, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	row := check.Entries[0]
	masterPost(t, router, cookie, path, url.Values{"action": {"complete"}, "subtask_id": {row.ID}, "completion_revision": {row.CompletionRevision}, "operation_id": {uuid.NewString()}}, 403)
	if _, err := auth.SaveRole(ctx, actor, role, "Renamed identical checklist grants", grants, 1); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, path, url.Values{"action": {"rename"}, "subtask_id": {row.ID}, "title": {"Renamed row"}, "operation_id": {uuid.NewString()}}, 303)
	masterPost(t, router, cookie, path, url.Values{"action": {"reorder"}, "order": {row.ID}, "operation_id": {uuid.NewString()}}, 303)
	masterPost(t, router, cookie, path, url.Values{"action": {"remove"}, "subtask_id": {row.ID}, "operation_id": {uuid.NewString()}}, 303)
	check, err = auth.Subtasks(ctx, actor, j.ID)
	if err != nil || len(check.Entries) != 0 {
		t.Fatalf("independent maintenance failed: %+v %v", check, err)
	}
}

func TestSubtaskMalformedPlanningRejectsWholeSaveHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained checklist").SaveX(ctx)
	if _, err := auth.SubmitSubtask(ctx, authorization.Actor{UserID: admin}, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "add", Title: "Must survive malformed input"}); err != nil {
		t.Fatal(err)
	}
	before := client.Job.GetX(ctx, j.ID).Subtasks
	masterPost(t, router, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/jobs/%d", j.ID), url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Must roll back"}, "subtasks": {`[{"title":`}}, 400)
	after := client.Job.GetX(ctx, j.ID)
	if after.Subtasks != before || after.JobType != j.JobType {
		t.Fatal("malformed input partially saved or removed checklist")
	}
}

func TestSubtaskWebFormPreservesIndependentCompletionRevision(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Independent form revision").SaveX(ctx)
	added, err := auth.SubmitSubtask(ctx, actor, j.ID, authorization.SubtaskCommand{OperationID: uuid.NewString(), Action: "add", Title: "Original task title"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	page := requestBody(t, router, cookie, "GET", path, 200)
	form := regexp.MustCompile(`(?s)<form[^>]*data-subtask-command[^>]*>.*?</form>`).FindString(page)
	if form == "" {
		t.Fatal("completion form not rendered")
	}
	values := url.Values{"action": {"complete"}}
	for _, field := range regexp.MustCompile(`name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(form, -1) {
		values.Set(field[1], field[2])
	}
	if values.Get("subtask_id") != added.SubtaskID || values.Get("completion_revision") == "" {
		t.Fatalf("wrong completion form: %s", form)
	}
	masterPost(t, router, cookie, path, url.Values{"action": {"rename"}, "subtask_id": {added.SubtaskID}, "title": {"Renamed without invalidating field intent"}, "operation_id": {uuid.NewString()}}, 303)
	masterPost(t, router, cookie, path, values, 303)
	check, err := auth.Subtasks(ctx, actor, j.ID)
	if err != nil || !check.Entries[0].Completed || check.Entries[0].Title != "Renamed without invalidating field intent" {
		t.Fatalf("browser form lost independent intent: %+v %v", check, err)
	}
}
