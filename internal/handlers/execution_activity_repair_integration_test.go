package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These are real mutation/read/export seams, not manually seeded activity rows.
func TestExecutionActivityRepairHTTP(t *testing.T) {
	for _, kind := range []string{"notes", "status", "subtasks"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Execution history owner").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Execution history job").SaveX(ctx)
			base := fmt.Sprintf("/jobs/%d", j.ID)
			token, err := sessions.CreateMobile(ctx, admin, "Execution history device")
			if err != nil {
				t.Fatal(err)
			}
			api := apiv1.NewRouter(pool, client, sessions)
			occurred := time.Date(2020, 2, 3, 4, 5, 6, 0, time.UTC)
			want := map[string]int{}
			wantFacts := map[string]map[string][2]string{}
			switch kind {
			case "notes":
				for _, text := range []string{"Original field observation", "Revised field observation"} {
					in := executionNotes(t, api, token, base, text)
					in["occurred_at"] = occurred.Format(time.RFC3339)
					accepted := executionAPI(t, api, token, "POST", base+"/field-notes", in, 200)
					if replay := executionAPI(t, api, token, "POST", base+"/field-notes", in, 200); replay != accepted {
						t.Fatal("replay changed receipt")
					}
				}
				executionAPI(t, api, token, "POST", base+"/field-notes", executionNotes(t, api, token, base, "Revised field observation"), 200)
				want["field_notes_updated"] = 2
				assertContains(t, executionAPI(t, api, token, "GET", base+"/field-notes/history", nil, 200), "Original field observation")
			case "status":
				in := executionIntent(t, api, token, base, "job:pending")
				in["occurred_at"] = occurred.Format(time.RFC3339)
				executionAPI(t, api, token, "POST", base+"/status", in, 200)
				executionAPI(t, api, token, "POST", base+"/status", in, 200)
				in["operation_id"] = newOperationKey()
				executionAPI(t, api, token, "POST", base+"/status", in, 200)
				want["status_transitioned"] = 1
				wantFacts["status_transitioned"] = map[string][2]string{"Status ID": {"", fmt.Sprint(in["status_id"])}}
			case "subtasks":
				post := func(values url.Values) {
					values.Set("operation_id", newOperationKey())
					masterPost(t, web, cookie, base+"/subtasks", values, 303)
					masterPost(t, web, cookie, base+"/subtasks", values, 303)
				}
				check := func() authorization.Checklist {
					var result struct{ Data authorization.Checklist }
					if err := json.Unmarshal([]byte(executionAPI(t, api, token, "GET", base+"/subtasks", nil, 200)), &result); err != nil {
						t.Fatal(err)
					}
					return result.Data
				}
				post(url.Values{"action": {"add"}, "title": {"Original checklist title"}})
				first := check().Entries[0]
				post(url.Values{"action": {"add"}, "title": {"Second checklist title"}})
				second := check().Entries[1]
				post(url.Values{"action": {"rename"}, "subtask_id": {first.ID}, "title": {"Revised checklist title"}})
				post(url.Values{"action": {"reorder"}, "order": {second.ID, first.ID}})
				for _, completed := range []bool{true, false} {
					entry := check().Entries[1]
					in := map[string]any{"operation_id": newOperationKey(), "completed": completed, "completion_revision": entry.CompletionRevision, "occurred_at": occurred.Format(time.RFC3339)}
					path := base + "/subtasks/" + first.ID
					executionAPI(t, api, token, "PATCH", path, in, 200)
					executionAPI(t, api, token, "PATCH", path, in, 200)
					in["operation_id"] = newOperationKey()
					executionAPI(t, api, token, "PATCH", path, in, 200)
				}
				post(url.Values{"action": {"remove"}, "subtask_id": {first.ID}})
				want = map[string]int{"subtask_added": 2, "subtask_renamed": 1, "subtasks_reordered": 1, "subtask_completed": 1, "subtask_uncompleted": 1, "subtask_removed": 1}
				wantFacts["subtask_renamed"] = map[string][2]string{"Subtask ID": {first.ID, first.ID}, "Subtask title": {"Original checklist title", "Revised checklist title"}}
				wantFacts["subtask_completed"] = map[string][2]string{"Subtask ID": {first.ID, first.ID}, "Completed": {"false", "true"}, "Completion actor": {"", fmt.Sprint(admin)}}
				wantFacts["subtask_uncompleted"] = map[string][2]string{"Subtask ID": {first.ID, first.ID}, "Completed": {"true", "false"}}
				wantFacts["subtask_removed"] = map[string][2]string{"Subtask ID": {first.ID, first.ID}, "Subtask title": {"Revised checklist title", "Revised checklist title"}}
				wantFacts["subtasks_reordered"] = map[string][2]string{"Subtask order": {fmt.Sprintf(`[{"id":%q,"sort_order":0},{"id":%q,"sort_order":1}]`, first.ID, second.ID), fmt.Sprintf(`[{"id":%q,"sort_order":0},{"id":%q,"sort_order":1}]`, second.ID, first.ID)}}
				if got := len(check().History); got != 7 {
					t.Fatalf("dedicated history: %d", got)
				}
			}
			var feed struct {
				Data struct {
					Records []authorization.ActivityRecord
				}
			}
			if err := json.Unmarshal([]byte(executionAPI(t, api, token, "GET", base+"/activity", nil, 200)), &feed); err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, event := range feed.Data.Records {
				if _, ok := want[event.Action]; !ok {
					continue
				}
				counts[event.Action]++
				for field, pair := range wantFacts[event.Action] {
					found := false
					for _, fact := range event.Facts {
						if fact.Field == field {
							found = true
							if fact.Before != pair[0] || fact.After != pair[1] {
								t.Errorf("%s %s: want %q -> %q, got %+v", event.Action, field, pair[0], pair[1], fact)
							}
						}
					}
					if !found {
						t.Errorf("%s missing %s", event.Action, field)
					}
				}
				if event.ActorID != admin || len(event.Facts) == 0 || event.Accepted.Before(time.Now().Add(-time.Minute)) {
					t.Errorf("missing original facts: %+v", event)
				}
				if kind != "subtasks" || event.Action == "subtask_completed" || event.Action == "subtask_uncompleted" {
					if event.Occurred == nil || !event.Occurred.Equal(occurred) {
						t.Errorf("lost historical occurrence: %+v", event)
					}
				}
				if kind == "notes" && counts[event.Action] == 1 {
					if len(event.Facts) != 1 || event.Facts[0].Before != "Original field observation" || event.Facts[0].After != "Revised field observation" {
						t.Errorf("imprecise Notes facts: %+v", event.Facts)
					}
				}
			}
			for action, n := range want {
				if counts[action] != n {
					t.Errorf("%s: want %d operational events, got %d", action, n, counts[action])
				}
			}
			filter := url.Values{"object_type": {"job"}, "object_id": {fmt.Sprint(j.ID)}}
			for _, path := range []string{base + "/activity", "/activity?" + filter.Encode()} {
				body := requestBody(t, web, cookie, "GET", path, 200)
				for action, n := range want {
					label := action
					switch action {
					case "field_notes_updated":
						label = "changed Field Notes on"
					case "subtask_completed":
						label = "completed a subtask on"
					case "subtask_uncompleted":
						label = "uncompleted a subtask on"
					}
					if strings.Count(body, ">"+label+"<") != n {
						t.Errorf("%s missing/duplicate %s", path, action)
					}
				}
			}
			filter.Set("scope", "A")
			filter.Set("operation_id", newOperationKey())
			exportPath := masterPost(t, web, cookie, "/activity/export", filter, 303)
			if worked, err := exports.Product(pool).ProcessOne(ctx); err != nil || !worked {
				t.Fatalf("export: %v %v", worked, err)
			}
			rows, err := csv.NewReader(strings.NewReader(requestBody(t, web, cookie, "GET", exportPath+"/download", 200))).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			counts = map[string]int{}
			for _, row := range rows[1:] {
				if _, ok := want[row[8]]; !ok {
					continue
				}
				counts[row[8]]++
				var facts []authorization.ActivityFact
				if err := json.Unmarshal([]byte(row[9]), &facts); err != nil {
					t.Fatal(err)
				}
				for field, pair := range wantFacts[row[8]] {
					found := false
					for _, fact := range facts {
						if fact.Field == field && fact.Before == pair[0] && fact.After == pair[1] {
							found = true
						}
					}
					if !found {
						t.Errorf("CSV %s missing precise %s: %v", row[8], field, facts)
					}
				}
				if row[3] != fmt.Sprint(admin) || row[9] == "[]" {
					t.Errorf("CSV missing actor/facts: %v", row)
				}
				if kind != "subtasks" || row[8] == "subtask_completed" || row[8] == "subtask_uncompleted" {
					if row[2] != occurred.Format(time.RFC3339) {
						t.Errorf("CSV lost occurrence: %v", row)
					}
				}
			}
			for action, n := range want {
				if counts[action] != n {
					t.Errorf("CSV %s: want %d got %d", action, n, counts[action])
				}
			}
		})
	}
}

func executionActivitySnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot string
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
	 'jobs',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM jobs t),
	 'notes',(SELECT jsonb_agg(to_jsonb(t) ORDER BY revision) FROM job_notes_history t),
	 'subtasks',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM job_subtasks t),
	 'subtask_history',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM subtask_history t),
	 'execution_receipts',(SELECT jsonb_agg(to_jsonb(t) ORDER BY operation_id) FROM job_execution_receipts t),
	 'subtask_receipts',(SELECT jsonb_agg(to_jsonb(t) ORDER BY operation_id) FROM subtask_operation_receipts t),
	 'admissions',(SELECT jsonb_agg(to_jsonb(t) ORDER BY operation_id) FROM operation_admissions t),
	 'audit',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM authorization_audit t),
	 'activity',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM activity_logs t))::text`).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestExecutionActivityRepairRollbackHTTP(t *testing.T) {
	for _, action := range []string{"notes", "status", "add", "rename", "reorder", "complete", "reopen", "remove"} {
		t.Run(action, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic history owner").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Atomic history job").SaveX(ctx)
			base := fmt.Sprintf("/jobs/%d", j.ID)
			token, err := sessions.CreateMobile(ctx, admin, "Atomic history device")
			if err != nil {
				t.Fatal(err)
			}
			api := apiv1.NewRouter(pool, client, sessions)
			for _, title := range []string{"First atomic task", "Second atomic task"} {
				masterPost(t, web, cookie, base+"/subtasks", url.Values{"operation_id": {newOperationKey()}, "action": {"add"}, "title": {title}}, 303)
			}
			var check struct{ Data authorization.Checklist }
			if err := json.Unmarshal([]byte(executionAPI(t, api, token, "GET", base+"/subtasks", nil, 200)), &check); err != nil {
				t.Fatal(err)
			}
			first, second := check.Data.Entries[0], check.Data.Entries[1]
			if action == "reopen" {
				masterPost(t, web, cookie, base+"/subtasks", url.Values{"operation_id": {newOperationKey()}, "action": {"complete"}, "subtask_id": {first.ID}, "completion_revision": {first.CompletionRevision}}, 303)
				if err := json.Unmarshal([]byte(executionAPI(t, api, token, "GET", base+"/subtasks", nil, 200)), &check); err != nil {
					t.Fatal(err)
				}
				first = check.Data.Entries[0]
			}
			op := newOperationKey()
			values := url.Values{"operation_id": {op}, "action": {action}, "subtask_id": {first.ID}, "title": {"Changed atomic task"}, "completion_revision": {first.CompletionRevision}}
			if action == "add" {
				values.Del("subtask_id")
			}
			if action == "reorder" {
				values.Del("subtask_id")
				values["order"] = []string{second.ID, first.ID}
			}
			path := base + "/subtasks"
			var intent map[string]any
			if action == "notes" {
				path = base + "/field-notes"
				intent = executionNotes(t, api, token, base, "Atomic observation")
			}
			if action == "status" {
				path = base + "/status"
				intent = executionIntent(t, api, token, base, "job:pending")
			}
			submit := func() *httptest.ResponseRecorder {
				if intent == nil {
					return jobRepairRawPost(web, cookie, path, values)
				}
				data, err := json.Marshal(intent)
				if err != nil {
					t.Fatal(err)
				}
				r := httptest.NewRequest("POST", path, strings.NewReader(string(data)))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				return w
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION execution_activity_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected operational activity failure'; END $$; CREATE TRIGGER execution_activity_fail BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION execution_activity_fail()`); err != nil {
				t.Fatal(err)
			}
			before := executionActivitySnapshot(t, pool)
			if w := submit(); w.Code < 500 {
				t.Fatalf("activity failure acknowledged effect: %d %s", w.Code, w.Body.String())
			}
			if after := executionActivitySnapshot(t, pool); after != before {
				t.Fatal("activity failure persisted effect/history/audit/receipt/admission")
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER execution_activity_fail ON activity_logs`); err != nil {
				t.Fatal(err)
			}
			want := 303
			if intent != nil {
				want = 200
			}
			if w := submit(); w.Code != want {
				t.Fatalf("original operation retry: %d %s", w.Code, w.Body.String())
			}
			accepted := executionActivitySnapshot(t, pool)
			if w := submit(); w.Code != want {
				t.Fatalf("replay: %d", w.Code)
			}
			if executionActivitySnapshot(t, pool) != accepted {
				t.Fatal("replay duplicated persistent facts")
			}
		})
	}
}

func TestExecutionActivityRepairCurrentAuthorityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private history owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Private history job").SaveX(ctx)
	base := fmt.Sprintf("/jobs/%d", j.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	masterPost(t, web, cookie, base+"/subtasks", url.Values{"operation_id": {newOperationKey()}, "action": {"add"}, "title": {"SECRET_CHECKLIST_HISTORY"}}, 303)
	notes := requestBody(t, web, cookie, "GET", base+"/field-notes", 200)
	masterPost(t, web, cookie, base+"/field-notes", url.Values{"operation_id": {newOperationKey()}, "notes_revision": {jobExecutionHidden(t, notes, "notes_revision")}, "field_notes": {"SECRET_NOTES_HISTORY"}}, 303)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Assigned history reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "activity.export", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Read only history reader").SetEmail("execution-history-reader@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	limited := sessionCookie(t, ctx, sessions, u.ID)
	for _, path := range []string{base + "/activity", "/activity?type=job"} {
		body := requestBody(t, web, limited, "GET", path, 200)
		assertContains(t, body, "SECRET_NOTES_HISTORY")
		assertContains(t, body, "SECRET_CHECKLIST_HISTORY")
	}
	filter := url.Values{"type": {"job"}, "scope": {"J"}, "operation_id": {newOperationKey()}}
	exportPath := masterPost(t, web, limited, "/activity/export", filter, 303)
	if worked, err := exports.Product(pool).ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("export: %v %v", worked, err)
	}
	body := requestBody(t, web, limited, "GET", exportPath+"/download", 200)
	assertContains(t, body, "SECRET_NOTES_HISTORY")
	assertContains(t, body, "SECRET_CHECKLIST_HISTORY")
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	requestBody(t, web, limited, "GET", base+"/activity", 403)
	assertNotContains(t, requestBody(t, web, limited, "GET", "/activity?type=job", 200), "SECRET_")
	requestBody(t, web, limited, "GET", exportPath+"/download", 410)
	filter.Set("operation_id", newOperationKey())
	exportPath = masterPost(t, web, limited, "/activity/export", filter, 303)
	if worked, err := exports.Product(pool).ProcessOne(ctx); err != nil || !worked {
		t.Fatalf("empty export: %v %v", worked, err)
	}
	assertNotContains(t, requestBody(t, web, limited, "GET", exportPath+"/download", 200), "SECRET_")
}

// This is the bounded execution-activity cutover, not a claim about the exact
// earlier transfer/planning source horizons covered by the history delegate.
func TestExecutionActivityRepairInstalledHTTP(t *testing.T) {
	old := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "083_execution_activity.up.sql" {
			continue
		}
		data, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[entry.Name()] = &fstest.MapFile{Data: data}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed execution owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Installed execution history").SaveX(ctx)
	base := fmt.Sprintf("/jobs/%d", j.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	notes := requestBody(t, web, cookie, "GET", base+"/field-notes", 200)
	masterPost(t, web, cookie, base+"/field-notes", url.Values{"operation_id": {newOperationKey()}, "notes_revision": {jobExecutionHidden(t, notes, "notes_revision")}, "field_notes": {"Retained installed observation"}}, 303)
	masterPost(t, web, cookie, base+"/subtasks", url.Values{"operation_id": {newOperationKey()}, "action": {"add"}, "title": {"Retained installed checklist"}}, 303)
	var source string
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM authorization_audit a),'notes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY revision) FROM job_notes_history n),'subtasks',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM subtask_history s))::text`).Scan(&source); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET name='CURRENT EDITOR IS NOT HISTORIC AUTHOR' WHERE id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
			t.Fatal(err)
		}
	}
	var retained string
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM authorization_audit a),'notes',(SELECT jsonb_agg(to_jsonb(n) ORDER BY revision) FROM job_notes_history n),'subtasks',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM subtask_history s))::text`).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if source != retained {
		t.Fatal("cutover rewrote source histories")
	}
	var exact int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs e JOIN authorization_audit a ON e.event_key='authorization-audit:'||a.id WHERE e.object_type='job' AND e.object_id=$1 AND e.actor_id=a.actor_id AND e.created_at=a.recorded_at AND e.metadata->'actor_name'=to_jsonb(a)->'actor_name'`, j.ID).Scan(&exact); err != nil || exact != 2 {
		t.Fatalf("backfill lost original identity/actor/acceptance or duplicated effects: %d %v", exact, err)
	}
	body := requestBody(t, web, cookie, "GET", base+"/activity", 200)
	assertContains(t, body, "Retained installed observation")
	assertContains(t, body, "Retained installed checklist")
	// The current user's navigation may show the new name; individual activity
	// rows must continue to show the captured original Admin actor.
	if strings.Count(body, "<strong>Admin</strong>") != 2 {
		t.Fatal("historic actor relabelled")
	}
}

func TestExecutionActivityRepairUnchangedReorderHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("No-op checklist owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("No-op checklist").SaveX(ctx)
	base := fmt.Sprintf("/jobs/%d", j.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	masterPost(t, web, cookie, base+"/subtasks", url.Values{"operation_id": {newOperationKey()}, "action": {"add"}, "title": {"Unchanged checklist"}}, 303)
	var id string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM job_subtasks WHERE job_id=$1`, j.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	masterPost(t, web, cookie, base+"/subtasks", url.Values{"operation_id": {newOperationKey()}, "action": {"reorder"}, "order": {id}}, 303)
	var histories, events int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM subtask_history WHERE job_id=$1),(SELECT count(*) FROM activity_logs WHERE object_type='job' AND object_id=$1)`, j.ID).Scan(&histories, &events); err != nil {
		t.Fatal(err)
	}
	if histories != 1 || events != 1 {
		t.Fatalf("unchanged order attributed a new effect: histories=%d events=%d", histories, events)
	}
}
