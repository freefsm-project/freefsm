package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The barrier observes the actual UPDATE while the domain transaction owns the
// authority fence. Fixture application, exact blocker, lock and SQL all match.
func waitFieldMutation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker int32, statement string) int32 {
	t.Helper()
	application := pool.Config().ConnConfig.RuntimeParams["application_name"]
	if application == "" {
		t.Fatal("missing fixture identity")
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var pid int32
		err := pool.QueryRow(ctx, `SELECT a.pid FROM pg_stat_activity a WHERE a.datname=current_database() AND a.application_name=$2 AND $1=ANY(pg_blocking_pids(a.pid)) AND a.query LIKE $3 AND a.wait_event='advisory'
		AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.classid=0 AND l.objid=810081 AND NOT l.granted)
		AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.classid=0 AND l.objid=190019 AND l.granted)`, blocker, application, statement+"%").Scan(&pid)
		if err == nil {
			t.Logf("field DML fixture=%s writer=%d blocker=%d statement=%s", application, pid, blocker, statement)
			return pid
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("field request never reached actual mutation barrier")
	return 0
}

func TestJobFieldSyncActualMutationRaceHTTP(t *testing.T) {
	for _, family := range []string{"status", "subtask"} {
		for _, apiFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/apiFirst=%v", family, apiFirst), func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Actual field race").SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Racing fields").SaveX(ctx)
				cookie := sessionCookie(t, ctx, sessions, admin)
				path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
				masterPost(t, router, cookie, path, url.Values{"action": {"add"}, "title": {"Racing task"}, "operation_id": {uuid.NewString()}}, 303)
				token, err := sessions.CreateMobile(ctx, admin, "Field DML race")
				if err != nil {
					t.Fatal(err)
				}
				api := apiv1.NewRouter(pool, client, sessions)
				var revision, meaning, target, subtask, completion string
				if err := pool.QueryRow(ctx, `SELECT j.status_revision::text,s.meaning_revision::text,s.id::text FROM jobs j JOIN statuses s ON s.company_id=j.company_id AND s.category_key='job:completed' WHERE j.id=$1`, j.ID).Scan(&revision, &meaning, &target); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT id::text,completion_revision::text FROM job_subtasks WHERE job_id=$1`, j.ID).Scan(&subtask, &completion); err != nil {
					t.Fatal(err)
				}
				op := map[string]any{"operation_id": uuid.NewString(), "type": "job.status", "target": fmt.Sprint(j.ID), "depends_on": []string{}, "status_id": target, "status_revision": revision, "meaning_revision": meaning}
				var webTarget, webMeaning string
				if err := pool.QueryRow(ctx, `SELECT id::text,meaning_revision::text FROM statuses WHERE company_id=$1 AND category_key='job:new'`, company).Scan(&webTarget, &webMeaning); err != nil {
					t.Fatal(err)
				}
				webPath := fmt.Sprintf("/jobs/%d/status", j.ID)
				form := url.Values{"operation_id": {uuid.NewString()}, "status_id": {webTarget}, "status_revision": {revision}, "meaning_" + webTarget: {webMeaning}}
				statement := "UPDATE jobs SET status_id="
				trigger := `CREATE TRIGGER field_barrier BEFORE UPDATE OF status_id ON jobs FOR EACH ROW WHEN(NEW.status_id IS DISTINCT FROM OLD.status_id) EXECUTE FUNCTION field_barrier()`
				if family == "subtask" {
					op = map[string]any{"operation_id": uuid.NewString(), "type": "job.subtask", "target": fmt.Sprint(j.ID), "depends_on": []string{}, "subtask_id": subtask, "completion_revision": completion, "completed": apiFirst}
					webPath = path
					form = url.Values{"operation_id": {uuid.NewString()}, "action": {"complete"}, "subtask_id": {subtask}, "completion_revision": {completion}}
					if apiFirst {
						form.Set("action", "reopen")
					}
					statement = "UPDATE job_subtasks SET completed="
					trigger = `CREATE TRIGGER field_barrier BEFORE UPDATE OF completed ON job_subtasks FOR EACH ROW WHEN(NEW.completed IS DISTINCT FROM OLD.completed) EXECUTE FUNCTION field_barrier()`
				}
				if _, err := pool.Exec(ctx, `CREATE FUNCTION field_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(810081); RETURN NEW; END $$;`+trigger); err != nil {
					t.Fatal(err)
				}
				barrier, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer barrier.Rollback(context.Background())
				var blocker int32
				if err := barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(810081)) x`).Scan(&blocker); err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(map[string]any{"operations": []any{op}})
				apiDone, webDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
				callAPI := func() {
					r := httptest.NewRequest("POST", "/sync/operations", strings.NewReader(string(body))).WithContext(ctx)
					r.Header.Set("Authorization", "Bearer "+token)
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					api.ServeHTTP(w, r)
					apiDone <- w
				}
				callWeb := func() {
					r := httptest.NewRequest("POST", webPath, strings.NewReader(form.Encode())).WithContext(ctx)
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(cookie)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, r)
					webDone <- w
				}
				if apiFirst {
					go callAPI()
				} else {
					go callWeb()
				}
				writer := waitFieldMutation(t, ctx, pool, blocker, statement)
				// A real foreign waiter cannot establish readiness for the contender.
				assertForeignAuthorityExcluded(t, ctx, pool, writer)
				if apiFirst {
					go callWeb()
				} else {
					go callAPI()
				}
				contender := waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
				if contender == writer {
					t.Fatal("writer reused as contender proof")
				}
				if err := barrier.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				aw, ww := <-apiDone, <-webDone
				wantAPI, wantWeb := "accepted", 409
				if !apiFirst {
					wantWeb = 303
					wantAPI = "status_reselection_required"
					if family == "subtask" {
						wantAPI = "subtask_conflict"
					}
				}
				if aw.Code != 200 || !strings.Contains(aw.Body.String(), `"code":"`+wantAPI+`"`) || ww.Code != wantWeb {
					t.Fatalf("competing state race: API%d %s web%d %s", aw.Code, aw.Body.String(), ww.Code, ww.Body.String())
				}
				var effects int
				query := `SELECT count(*) FROM authorization_audit WHERE target_type='job' AND target_id=$1 AND action='job.status'`
				if family == "subtask" {
					query = `SELECT count(*) FROM subtask_history WHERE job_id=$1 AND action='complete'`
				}
				if err := pool.QueryRow(ctx, query, j.ID).Scan(&effects); err != nil || effects != 1 {
					t.Fatalf("duplicate domain effect/history: %d %v", effects, err)
				}
				var retained bool
				if family == "subtask" {
					if err := pool.QueryRow(ctx, `SELECT completed FROM job_subtasks WHERE id=$1`, subtask).Scan(&retained); err != nil || !retained {
						t.Fatalf("winner completion overwritten: %v", err)
					}
				} else {
					winner := webTarget
					if apiFirst {
						winner = target
					}
					if err := pool.QueryRow(ctx, `SELECT status_id::text=$2 FROM jobs WHERE id=$1`, j.ID, winner).Scan(&retained); err != nil || !retained {
						t.Fatalf("winner Status overwritten: %v", err)
					}
				}
			})
		}
	}
}

func TestJobFieldSyncAuditRollbackAndConcurrentReplayHTTP(t *testing.T) {
	for _, family := range []string{"status", "subtask"} {
		t.Run(family, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic field action").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Atomic").SaveX(ctx)
			client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
			masterPost(t, router, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/jobs/%d/subtasks", j.ID), url.Values{"action": {"add"}, "title": {"Atomic task"}, "operation_id": {uuid.NewString()}}, 303)
			token, err := sessions.CreateMobile(ctx, admin, "Field atomicity")
			if err != nil {
				t.Fatal(err)
			}
			api := apiv1.NewRouter(pool, client, sessions)
			row := syncGet(t, api, token, "", 200).Records[0]
			def := row["status_definitions"].([]any)[0].(map[string]any)
			op := map[string]any{"operation_id": uuid.NewString(), "type": "job.status", "target": row["id"], "depends_on": []string{}, "status_id": def["id"], "status_revision": row["status_revision"], "meaning_revision": def["meaning_revision"]}
			action := "job.status"
			if family == "subtask" {
				task := row["subtasks"].([]any)[0].(map[string]any)
				op = map[string]any{"operation_id": uuid.NewString(), "type": "job.subtask", "target": row["id"], "depends_on": []string{}, "subtask_id": task["id"], "completion_revision": task["completion_revision"], "completed": true}
				action = "job.subtask.complete"
			}
			body, _ := json.Marshal(map[string]any{"operations": []any{op}})
			if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_field_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'field audit unavailable'; END $$; CREATE TRIGGER fail_field_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_field_audit()`); err != nil {
				t.Fatal(err)
			}
			w := batchRequest(api, token, string(body))
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"retryable_failure"`) || strings.Contains(w.Body.String(), `"receipt"`) {
				t.Fatalf("false terminal failure: %d %s", w.Code, w.Body.String())
			}
			var admissions int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_admissions WHERE operation_id=$1`, op["operation_id"]).Scan(&admissions); err != nil || admissions != 0 {
				t.Fatalf("partial admission: %d %v", admissions, err)
			}
			var unchanged bool
			if err := pool.QueryRow(ctx, `SELECT status_revision::text=$2 AND NOT EXISTS(SELECT 1 FROM job_subtasks WHERE job_id=$1 AND completed) FROM jobs WHERE id=$1`, j.ID, row["status_revision"]).Scan(&unchanged); err != nil || !unchanged {
				t.Fatalf("audit failure retained a domain mutation: %v", err)
			}
			if _, err := pool.Exec(ctx, `DROP TRIGGER fail_field_audit ON authorization_audit`); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			results := make(chan *httptest.ResponseRecorder, 6)
			for i := 0; i < 6; i++ {
				wg.Add(1)
				go func() { defer wg.Done(); results <- batchRequest(api, token, string(body)) }()
			}
			wg.Wait()
			close(results)
			var original string
			for result := range results {
				if result.Code != 200 || !strings.Contains(result.Body.String(), `"code":"accepted"`) {
					t.Fatalf("retry failed: %d %s", result.Code, result.Body.String())
				}
				if original == "" {
					original = result.Body.String()
				} else if original != result.Body.String() {
					t.Fatal("same-ID receipt changed")
				}
			}
			var audits int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_id=$1 AND action=$2`, j.ID, action).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("duplicate audit: %d %v", audits, err)
			}
		})
	}
}

func TestJobFieldSyncIndependentGrantsAndClosedEnvelopeHTTP(t *testing.T) {
	f, api, token, _, role, ids := syncFixture(t, 1)
	ctx := context.Background()
	path := fmt.Sprintf("/jobs/%d/subtasks", ids[0])
	masterPost(t, f.router, f.cookie, path, url.Values{"action": {"add"}, "title": {"Private task comparison"}, "operation_id": {uuid.NewString()}}, 303)
	row := syncGet(t, api, token, "", 200).Records[0]
	task := row["subtasks"].([]any)[0].(map[string]any)
	definition := row["status_definitions"].([]any)[0].(map[string]any)
	status := map[string]any{"operation_id": uuid.NewString(), "type": "job.status", "target": row["id"], "depends_on": []string{}, "status_id": definition["id"], "status_revision": row["status_revision"], "meaning_revision": definition["meaning_revision"]}
	complete := map[string]any{"operation_id": uuid.NewString(), "type": "job.subtask", "target": row["id"], "depends_on": []string{}, "subtask_id": task["id"], "completion_revision": task["completion_revision"], "completed": true}
	submit := func(op map[string]any, want string) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"operations": []any{op}})
		w := batchRequest(api, token, string(body))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"`+want+`"`) || strings.Contains(w.Body.String(), "Private task comparison") {
			t.Fatalf("grant result%d %s want%s", w.Code, w.Body.String(), want)
		}
	}
	submit(status, "forbidden")
	submit(complete, "forbidden")
	auth := authorization.New(f.pool)
	var roleRevision int64
	if err := f.pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&roleRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "Independent field grants", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.status", Scope: authorization.Assigned}, {Permission: "job.subtask.complete", Scope: authorization.Assigned}}, roleRevision); err != nil {
		t.Fatal(err)
	}
	// Terminal denials remain terminal even after the Role acquires a grant.
	submit(status, "forbidden")
	submit(complete, "forbidden")
	status["operation_id"] = uuid.NewString()
	complete["operation_id"] = uuid.NewString()
	submit(status, "accepted")
	submit(complete, "accepted")
	complete["operation_id"] = uuid.NewString()
	complete["completed"] = false
	submit(complete, "forbidden")
	// A later planning property or null dependency rejects the entire envelope,
	// including the valid earlier member's admission.
	for _, property := range []string{"title", "notes_revision", "null_dependency"} {
		first := map[string]any{"operation_id": uuid.NewString(), "type": "job.status", "target": row["id"], "depends_on": []string{}, "status_id": definition["id"], "status_revision": row["status_revision"], "meaning_revision": definition["meaning_revision"]}
		bad := map[string]any{"operation_id": uuid.NewString(), "type": "job.subtask", "target": row["id"], "depends_on": []string{}, "subtask_id": task["id"], "completion_revision": task["completion_revision"], "completed": true}
		if property == "null_dependency" {
			bad["depends_on"] = []any{nil}
		} else {
			bad[property] = ""
		}
		body, _ := json.Marshal(map[string]any{"operations": []any{first, bad}})
		w := batchRequest(api, token, string(body))
		if w.Code != 400 {
			t.Fatalf("invalid %s envelope: %d %s", property, w.Code, w.Body.String())
		}
		var n int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM operation_admissions WHERE operation_id=$1`, first["operation_id"]).Scan(&n); err != nil || n != 0 {
			t.Fatalf("earlier admission before preflight: %d %v", n, err)
		}
	}
}
