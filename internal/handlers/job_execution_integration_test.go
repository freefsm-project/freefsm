package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/justinas/nosurf"
)

func executionAPI(t *testing.T, api http.Handler, token, method, path string, input any, want int) string {
	t.Helper()
	data, _ := json.Marshal(input)
	r := httptest.NewRequest(method, path, strings.NewReader(string(data)))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w.Body.String()
}

func TestJobExecutionLostHTTPResponseThenScopeLossReceipt(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lost response field actor", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.field_notes.edit", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Lost response actor").SetEmail("lost39@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Private execution context").SaveX(ctx)
	if _, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, u.ID, "Lost response device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	intent := executionNotes(t, api, token, path, "PRIVATE_COMMITTED_NOTE")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Lost-Response") == "yes" {
			accepted := httptest.NewRecorder()
			api.ServeHTTP(accepted, r)
			if accepted.Code != 200 {
				t.Errorf("effect not accepted before lost response: %d", accepted.Code)
				w.WriteHeader(500)
				return
			}
			if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	body, _ := json.Marshal(intent)
	request, err := http.NewRequest("POST", server.URL+path+"/field-notes", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Test-Lost-Response", "yes")
	response, err := server.Client().Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("HTTP acknowledgement was not actually lost")
	}
	lookup, err := http.NewRequest("GET", server.URL+"/job-execution-operations/"+intent["operation_id"].(string), nil)
	if err != nil {
		t.Fatal(err)
	}
	lookup.Header.Set("Authorization", "Bearer "+token)
	response, err = server.Client().Do(lookup)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.Contains(string(receipt), `"code":"accepted"`) || !strings.Contains(string(receipt), `"effect_id"`) {
		t.Fatalf("original lost operation unresolved: %d %s", response.StatusCode, receipt)
	}
	if strings.Contains(string(receipt), "PRIVATE") || strings.Contains(string(receipt), "Private execution") {
		t.Fatal("receipt disclosed inaccessible text or context")
	}
	executionAPI(t, api, token, "GET", path+"/field-notes", nil, 404)
	executionAPI(t, api, token, "GET", path+"/field-notes/history", nil, 404)
	replay := executionAPI(t, api, token, "POST", path+"/field-notes", intent, 200)
	if replay != string(receipt) {
		t.Fatal("replay changed immutable receipt after lost response/access")
	}
	intent["operation_id"] = uuid.NewString()
	executionAPI(t, api, token, "POST", path+"/field-notes", intent, 404)
}

func TestJobExecutionUpgradePreservesRecordedFacts(t *testing.T) {
	var jobID, oldStatus int64
	pool, _, router, sessions, _, admin := permissionHTTPFixtureSeeded(t, func(pool *pgxpool.Pool) {
		err := pool.QueryRow(context.Background(), `WITH c AS (INSERT INTO customers(company_id,display_name) SELECT id,'Pre-v1 owner' FROM companies LIMIT 1 RETURNING id,company_id)
   INSERT INTO jobs(company_id,customer_id,job_type,notes,tech_notes,status_id) SELECT company_id,id,'Original title','Office planning text','Unattributed legacy field text',(SELECT id FROM statuses WHERE category_key='job:pending' AND company_id=c.company_id LIMIT 1) FROM c RETURNING id,status_id`).Scan(&jobID, &oldStatus)
		if err != nil {
			t.Fatal(err)
		}
	})
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/field-notes", jobID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Unattributed legacy field text")
	assertContains(t, body, "historical author and acceptance time unknown")
	var status int64
	var planning, notes string
	var unknown bool
	err := pool.QueryRow(ctx, `SELECT j.status_id,j.notes,j.tech_notes,h.actor_id IS NULL AND h.accepted_at IS NULL AND h.baseline FROM jobs j JOIN job_notes_history h ON h.job_id=j.id WHERE j.id=$1`, jobID).Scan(&status, &planning, &notes, &unknown)
	if err != nil || status != oldStatus || planning != "Office planning text" || notes != "Unattributed legacy field text" || !unknown {
		t.Fatalf("legacy facts changed: %v %d %q %q %v", err, status, planning, notes, unknown)
	}
	masterPost(t, router, cookie, path, url.Values{"operation_id": {uuid.NewString()}, "notes_revision": {jobExecutionHidden(t, body, "baseline_revision")}, "field_notes": {"Must not overwrite unreviewed baseline"}}, 409)
}

func TestJobExecutionIndependentGrantMatrixAndArchiveAPI(t *testing.T) {
	for _, scope := range []authorization.Scope{authorization.All, authorization.Assigned} {
		for _, permission := range []string{"job.status", "job.field_notes.edit"} {
			t.Run(string(scope)+"/"+permission, func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				auth := authorization.New(pool)
				role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Arbitrary display name", []authorization.Grant{{Permission: "job.view", Scope: scope}, {Permission: permission, Scope: scope}})
				if err != nil {
					t.Fatal(err)
				}
				u := client.User.Create().SetCompanyID(company).SetName("Independent actor").SetEmail("matrix39@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
				if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
					t.Fatal(err)
				}
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
				j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Independent grants").SaveX(ctx)
				if scope == authorization.Assigned {
					if _, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
						t.Fatal(err)
					}
				}
				token, err := sessions.CreateMobile(ctx, u.ID, "Matrix")
				if err != nil {
					t.Fatal(err)
				}
				api := apiv1.NewRouter(pool, client, sessions)
				path := fmt.Sprintf("/jobs/%d", j.ID)
				status := executionIntent(t, api, token, path, "job:completed")
				notes := executionNotes(t, api, token, path, "Independent notes")
				statusWant, notesWant := 403, 403
				if permission == "job.status" {
					statusWant = 200
				} else {
					notesWant = 200
				}
				executionAPI(t, api, token, "POST", path+"/status", status, statusWant)
				executionAPI(t, api, token, "POST", path+"/field-notes", notes, notesWant)
				cookie := sessionCookie(t, ctx, sessions, u.ID)
				masterPost(t, web, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Forbidden planning"}}, 403)
				executionAPI(t, api, token, "GET", path+"/field-notes/history", nil, 200)
				blocked := executionNotes(t, api, token, path, "After archive")
				blockedStatus := executionIntent(t, api, token, path, "job:new")
				if _, err = pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, j.ID); err != nil {
					t.Fatal(err)
				}
				executionAPI(t, api, token, "POST", path+"/field-notes", blocked, 404)
				executionAPI(t, api, token, "POST", path+"/status", blockedStatus, 404)
				if _, err = pool.Exec(ctx, `UPDATE jobs SET deleted_at=NULL WHERE id=$1`, j.ID); err != nil {
					t.Fatal(err)
				}
				// Restoring authority never reopens an immutable terminal operation.
				executionAPI(t, api, token, "POST", path+"/field-notes", blocked, 404)
				executionAPI(t, api, token, "POST", path+"/status", blockedStatus, 404)
				if permission == "job.field_notes.edit" {
					blocked["operation_id"] = uuid.NewString()
					executionAPI(t, api, token, "POST", path+"/field-notes", blocked, 200)
				}
			})
		}
	}
}

// Older workflow scenarios now submit the same client-generated operation ID
// contract as production. Each explicit test submission is a new reviewed intent.
func newExecutionWireIntent(body string) string {
	var intent map[string]any
	if json.Unmarshal([]byte(body), &intent) != nil {
		return body
	}
	intent["operation_id"] = uuid.NewString()
	encoded, _ := json.Marshal(intent)
	return string(encoded)
}

func TestJobExecutionPersistedBrowserAndOfflineReference(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser execution owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Browser execution").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Executable offline reference")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", apiv1.NewRouter(pool, client, sessions)))
	web := nosurf.New(middleware.CSRFToken(router))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Lose-Response") == "once" {
			captured := httptest.NewRecorder()
			web.ServeHTTP(captured, r)
			if captured.Code != 303 {
				t.Errorf("lost-response request was not accepted: %d %s", captured.Code, captured.Body.String())
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = connection.Close()
			}
			return
		}
		web.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/job-execution.cjs", server.URL, cookie.Value, fmt.Sprint(j.ID), token).CombinedOutput()
	t.Logf("browser/offline: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}

func executionIntent(t *testing.T, api http.Handler, token, path, category string) map[string]any {
	t.Helper()
	var choices struct {
		Data []struct {
			Status struct{ Category string }
			Intent map[string]any
		}
	}
	if err := json.Unmarshal([]byte(executionAPI(t, api, token, "GET", path+"/status-intents", nil, 200)), &choices); err != nil {
		t.Fatal(err)
	}
	for _, choice := range choices.Data {
		if choice.Status.Category == category {
			choice.Intent["operation_id"] = uuid.NewString()
			return choice.Intent
		}
	}
	t.Fatal("missing semantic target " + category)
	return nil
}

func executionNotes(t *testing.T, api http.Handler, token, path, text string) map[string]any {
	t.Helper()
	var state struct {
		Data authorization.JobExecutionState
	}
	if err := json.Unmarshal([]byte(executionAPI(t, api, token, "GET", path+"/field-notes", nil, 200)), &state); err != nil {
		t.Fatal(err)
	}
	return map[string]any{"operation_id": uuid.NewString(), "notes_revision": state.Data.NotesRevision, "field_notes": text}
}

func TestJobExecutionSemanticStatusAndABAAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Semantic owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Semantics").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Semantics")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	pending := executionIntent(t, api, token, path, "job:pending")
	initial := executionIntent(t, api, token, path, "job:new")
	svc := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	target := int64(pending["status_id"].(float64))
	if _, err = svc.Update(ctx, actor, target, "Cosmetic rename", "#123456"); err != nil {
		t.Fatal(err)
	}
	accepted := executionAPI(t, api, token, "POST", path+"/status", pending, 200)
	pending["operation_id"] = uuid.NewString()
	noop := executionAPI(t, api, token, "POST", path+"/status", pending, 200)
	var a, b struct {
		Data authorization.JobExecutionReceipt
	}
	json.Unmarshal([]byte(accepted), &a)
	json.Unmarshal([]byte(noop), &b)
	if a.Data.EffectID != b.Data.EffectID {
		t.Fatal("same valid target altered attribution/effect")
	}
	executionAPI(t, api, token, "POST", path+"/status", initial, 409)
	currentNew := executionIntent(t, api, token, path, "job:new")
	executionAPI(t, api, token, "POST", path+"/status", currentNew, 200)
	pending["operation_id"] = uuid.NewString()
	executionAPI(t, api, token, "POST", path+"/status", pending, 409)
	fresh := executionIntent(t, api, token, path, "job:pending")
	replacement, err := svc.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Job, Name: "Remaining pending", Category: statusflow.JobPending})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Move(ctx, actor, statusflow.MoveRequest{StatusID: target, Category: statusflow.JobInProgress, SourceReplacementID: replacement.ID}); err != nil {
		t.Fatal(err)
	}
	if err = svc.Move(ctx, actor, statusflow.MoveRequest{StatusID: target, Category: statusflow.JobPending}); err != nil {
		t.Fatal(err)
	}
	executionAPI(t, api, token, "POST", path+"/status", fresh, 409)
	// Active finished Jobs remain writable in both directions.
	for _, category := range []string{"job:completed", "job:canceled", "job:in_progress"} {
		executionAPI(t, api, token, "POST", path+"/status", executionIntent(t, api, token, path, category), 200)
		executionAPI(t, api, token, "POST", path+"/field-notes", executionNotes(t, api, token, path, category), 200)
	}
	first := executionNotes(t, api, token, path, "A")
	executionAPI(t, api, token, "POST", path+"/field-notes", first, 200)
	stale := executionNotes(t, api, token, path, "Old writer")
	executionAPI(t, api, token, "POST", path+"/field-notes", executionNotes(t, api, token, path, "B"), 200)
	executionAPI(t, api, token, "POST", path+"/field-notes", executionNotes(t, api, token, path, "A"), 200)
	executionAPI(t, api, token, "POST", path+"/field-notes", stale, 409)
}

func TestJobExecutionConcurrentAndAuditFailureAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Concurrent owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Concurrent field work").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Concurrency")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	a := executionNotes(t, api, token, path, "First concurrent intent")
	b := executionNotes(t, api, token, path, "Second concurrent intent")
	codes := make(chan int, 2)
	for _, input := range []map[string]any{a, b} {
		go func(in map[string]any) {
			data, _ := json.Marshal(in)
			r := httptest.NewRequest("POST", path+"/field-notes", strings.NewReader(string(data)))
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			codes <- w.Code
		}(input)
	}
	x, y := <-codes, <-codes
	if !(x == 200 && y == 409 || x == 409 && y == 200) {
		t.Fatalf("concurrent same facet: %d %d", x, y)
	}
	concurrent := func(facet string, input map[string]any) {
		data, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", path+"/"+facet, strings.NewReader(string(data)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		codes <- w.Code
	}
	independentNotes := executionNotes(t, api, token, path, "Independent concurrent notes")
	independentStatus := executionIntent(t, api, token, path, "job:in_progress")
	go concurrent("field-notes", independentNotes)
	go concurrent("status", independentStatus)
	if x, y := <-codes, <-codes; x != 200 || y != 200 {
		t.Fatalf("independent facets conflicted: %d %d", x, y)
	}
	statusA := executionIntent(t, api, token, path, "job:completed")
	statusB := executionIntent(t, api, token, path, "job:canceled")
	go concurrent("status", statusA)
	go concurrent("status", statusB)
	if x, y := <-codes, <-codes; !(x == 200 && y == 409 || x == 409 && y == 200) {
		t.Fatalf("concurrent Status overwrite: %d %d", x, y)
	}
	in := executionNotes(t, api, token, path, "Audit must reject")
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_execution_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='job.field_notes.edit' THEN RAISE EXCEPTION 'audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_execution_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_execution_audit()`); err != nil {
		t.Fatal(err)
	}
	executionAPI(t, api, token, "POST", path+"/field-notes", in, 503)
	executionAPI(t, api, token, "GET", "/job-execution-operations/"+in["operation_id"].(string), nil, 404)
	body := executionAPI(t, api, token, "GET", path+"/field-notes/history", nil, 200)
	if strings.Contains(body, "Audit must reject") {
		t.Fatal("audit failure published history")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_execution_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	executionAPI(t, api, token, "POST", path+"/field-notes", in, 200)
	for _, table := range []string{"job_notes_history", "job_execution_receipts"} {
		if _, err = pool.Exec(ctx, `DELETE FROM `+table); err == nil {
			t.Fatal("mutable history/receipt: " + table)
		}
	}
}

func TestJobExecutionWaitingAuthorityLossAPI(t *testing.T) {
	for _, loss := range []string{"session", "archive", "assignment", "grant"} {
		t.Run(loss, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			auth := authorization.New(pool)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Waiting writer", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.field_notes.edit", Scope: authorization.Assigned}})
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetName("Waiter").SetEmail("wait39@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
			if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Waiter work").SaveX(ctx)
			if _, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
				t.Fatal(err)
			}
			token, err := sessions.CreateMobile(ctx, u.ID, "Waiter")
			if err != nil {
				t.Fatal(err)
			}
			api := apiv1.NewRouter(pool, client, sessions)
			path := fmt.Sprintf("/jobs/%d", j.ID)
			in := executionNotes(t, api, token, path, "Must not publish")
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var pid int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) fence`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				data, _ := json.Marshal(in)
				r := httptest.NewRequest("POST", path+"/field-notes", strings.NewReader(string(data)))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				result <- w
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				var waiting bool
				if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("request failed to reach authority fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch loss {
			case "session":
				_, err = tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, u.ID)
			case "archive":
				_, err = tx.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, j.ID)
			case "assignment":
				_, err = tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID)
			case "grant":
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.field_notes.edit'`, role)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w := <-result
			if w.Code != 403 && w.Code != 404 && w.Code != 401 {
				t.Fatalf("authority loss %s: %d %s", loss, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "Must not publish") {
				t.Fatal("inaccessible comparison leaked")
			}
			if client.Job.GetX(ctx, j.ID).TechNotes != "" {
				t.Fatal("authority loss committed effect")
			}
		})
	}
}

func TestJobExecutionAndPlanningCoexistHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Before planning").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	masterPost(t, router, cookie, "/jobs", url.Values{"form_mode": {"create"}, "job_id": {"0"}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"Unreviewed field injection"}, "tech_notes": {"Must use independent Field Notes"}}, 409)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	plan := currentJobFormValues(t, router, cookie, path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c.ID)}, "job_type": {"After planning"}})
	notes := requestBody(t, router, cookie, "GET", path+"/field-notes", 200)
	masterPost(t, router, cookie, path+"/field-notes", url.Values{"operation_id": {uuid.NewString()}, "notes_revision": {jobExecutionHidden(t, notes, "notes_revision")}, "field_notes": {"Independent observation"}}, 303)
	masterPost(t, router, cookie, path, plan, 303)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "After planning")
	assertContains(t, body, "Independent observation")
}

func TestJobExecutionCanonicalGrantsAndReceiptAccessLossAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Renamed field operator", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.field_notes.edit", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetName("Assigned writer").SetEmail("field39@test").SetPasswordHash("hash").SetRole("dispatcher").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned field work").SaveX(ctx)
	if _, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, user.ID, "Field client")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	executionAPI(t, api, token, "GET", path, nil, 200)
	var state struct {
		Data authorization.JobExecutionState
	}
	json.Unmarshal([]byte(executionAPI(t, api, token, "GET", path+"/field-notes", nil, 200)), &state)
	in := map[string]any{"operation_id": uuid.NewString(), "notes_revision": state.Data.NotesRevision, "field_notes": "Sensitive observation"}
	executionAPI(t, api, token, "POST", path+"/field-notes", in, 200)
	op := in["operation_id"].(string)
	if _, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	executionAPI(t, api, token, "GET", path, nil, 404)
	executionAPI(t, api, token, "GET", path+"/field-notes/history", nil, 404)
	receipt := executionAPI(t, api, token, "GET", "/job-execution-operations/"+op, nil, 200)
	if strings.Contains(receipt, "Sensitive") || strings.Contains(receipt, "field_notes") {
		t.Fatal("receipt leaked content")
	}
	assertContains(t, receipt, `"code":"accepted"`)
	in["operation_id"] = uuid.NewString()
	executionAPI(t, api, token, "POST", path+"/field-notes", in, 404)
	executionAPI(t, api, token, "GET", "/job-execution-operations/"+in["operation_id"].(string), nil, 200)
	executionAPI(t, api, token, "DELETE", "/session", nil, 204)
	executionAPI(t, api, token, "GET", "/job-execution-operations/"+op, nil, 401)
	freshToken, err := sessions.CreateMobile(ctx, user.ID, "Freshly authenticated client")
	if err != nil {
		t.Fatal(err)
	}
	if got := executionAPI(t, api, freshToken, "GET", "/job-execution-operations/"+op, nil, 200); got != receipt {
		t.Fatal("fresh sign-in lost immutable nonsensitive receipt")
	}
	in["operation_id"] = op
	executionAPI(t, api, freshToken, "POST", path+"/field-notes", in, 409)
}

func TestJobStatusReceiptAPI(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Status owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Status work").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Execution reference")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d/status", j.ID)
	var choices struct {
		Data []struct {
			Status struct {
				Category string `json:"category"`
			}
			Intent map[string]any
		}
	}
	if err = json.Unmarshal([]byte(executionAPI(t, api, token, "GET", path+"-intents", nil, 200)), &choices); err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	for _, choice := range choices.Data {
		if choice.Status.Category == "job:completed" {
			in = choice.Intent
		}
	}
	if in == nil {
		t.Fatal("no Completed target")
	}
	op := uuid.NewString()
	in["operation_id"] = op
	accepted := executionAPI(t, api, token, "POST", path, in, 200)
	assertContains(t, accepted, `"effect_id"`)
	if replay := executionAPI(t, api, token, "POST", path, in, 200); replay != accepted {
		t.Fatal("replay changed receipt")
	}
	receipt := executionAPI(t, api, token, "GET", "/job-execution-operations/"+op, nil, 200)
	if receipt != accepted {
		t.Fatal("lookup did not reconcile original operation")
	}
	activity := requestBody(t, web, sessionCookie(t, ctx, sessions, admin), "GET", fmt.Sprintf("/jobs/%d/activity", j.ID), 200)
	if strings.Count(activity, "status_transitioned") != 1 {
		t.Fatal("Status receipt path lost or duplicated the existing operational activity event")
	}
	in["status_revision"] = uuid.NewString()
	executionAPI(t, api, token, "POST", path, in, 409)
}

func jobExecutionHidden(t *testing.T, body, name string) string {
	t.Helper()
	m := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(body)
	if len(m) != 2 {
		t.Fatalf("missing %s in %s", name, body)
	}
	return m[1]
}

func TestJobFieldNotesIndependentRevisionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Field owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Field work").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/field-notes", j.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	rev := jobExecutionHidden(t, body, "notes_revision")
	input := url.Values{"operation_id": {uuid.NewString()}, "notes_revision": {rev}, "field_notes": {"Observed on site"}}
	masterPost(t, router, cookie, path, input, 303)
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Observed on site")
	if next := jobExecutionHidden(t, body, "notes_revision"); next == rev || next == "" {
		t.Fatal("accepted edit did not advance independent revision")
	}
	input.Set("operation_id", uuid.NewString())
	input.Set("field_notes", "Stale overwrite")
	masterPost(t, router, cookie, path, input, 409)
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Observed on site")
}

func TestJobFieldNotesLegacyReviewHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Legacy owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained work").SetTechNotes("Recorded before attributable revisions").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/field-notes", j.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Field Notes baseline — historical author and acceptance time unknown")
	revision := jobExecutionHidden(t, body, "baseline_revision")
	masterPost(t, router, cookie, path+"/review", url.Values{"baseline_revision": {revision}, "reason": {"Reviewed source records; unknown attribution retained"}}, 303)
	body = requestBody(t, router, cookie, "GET", path, 200)
	masterPost(t, router, cookie, path, url.Values{"operation_id": {uuid.NewString()}, "notes_revision": {jobExecutionHidden(t, body, "notes_revision")}, "field_notes": {"First attributable edit"}}, 303)
	body = requestBody(t, router, cookie, "GET", path+"/history", 200)
	assertContains(t, body, "Recorded before attributable revisions")
	assertContains(t, body, "First attributable edit")
	assertContains(t, body, "Admin")
}
