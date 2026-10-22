package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func commentPost(t *testing.T, router http.Handler, cookie *http.Cookie, path string, form url.Values, want int) string {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("POST %s: %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w.Body.String()
}

func TestCommentsAtomicAuditFailureAndConcurrentClosureHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic comments").SaveX(ctx)
	path := fmt.Sprintf("/customers/%d/comments", c.ID)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_comment_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='comment.add' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_comment_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_comment_audit()`); err != nil {
		t.Fatal(err)
	}
	failedIntent := url.Values{"content": {"must roll back"}, "operation_id": {"04500000-0000-4000-8000-000000000090"}}
	commentPost(t, router, cookie, path, failedIntent, 503)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial comment n=%d err=%v", n, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comment_operation_receipts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("transient failure published receipt n=%d err=%v", n, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_comment_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	commentPost(t, router, cookie, path, failedIntent, 200)
	commentPost(t, router, cookie, path, failedIntent, 200)
	commentPost(t, router, cookie, path, url.Values{"content": {"thread"}}, 200)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM comments WHERE content='thread'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, request := range []struct {
		path string
		form url.Values
	}{{path, url.Values{"content": {"racing reply"}, "reply_to": {fmt.Sprint(id)}}}, {fmt.Sprintf("%s/%d/delete", path, id), nil}} {
		go func(path string, form url.Values) {
			<-start
			r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			results <- w.Code
		}(request.path, request.form)
	}
	close(start)
	for range 2 {
		code := <-results
		if code != 200 && code != 409 {
			t.Fatalf("race status %d", code)
		}
	}
	body := requestBody(t, router, cookie, "GET", path, 200)
	if !strings.Contains(body, "Removed by Admin") {
		t.Fatal("closure not persisted")
	}
	commentPost(t, router, cookie, path, url.Values{"content": {"late reply"}, "reply_to": {fmt.Sprint(id)}}, 409)
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments r JOIN comments p ON p.id=r.reply_to WHERE r.created_at>p.removed_at`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("reply published after closure n=%d err=%v", n, err)
	}
}

func TestCommentsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser comments", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "comment.add", Scope: authorization.All}, {Permission: "comment.remove", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("comment-browser@test").SetName("Browser author").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser comments parent").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(browserCtx, "node", "testdata/browser/comments.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE object_id=$1 AND removed_at IS NOT NULL`, c.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("removed count=%d err=%v", n, err)
	}
}

func TestCommentsOfflineReferenceHarness(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node required for reference harness")
	}
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Harness parent").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Harness Job").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "OFF-05 reference harness")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "node", "testdata/browser/comment-reference-harness.cjs", server.URL, token, fmt.Sprint(j.ID), filepath.Join(t.TempDir(), "pending.json")).CombinedOutput()
	t.Logf("reference harness: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCommentsAllAvailableParentsAndIndependentRemovalHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Parent matrix").SaveX(ctx)
	aType := client.AssetType.Create().SetCompanyID(company).SetName("Comment equipment").SaveX(ctx)
	targets := []struct{ kind, path, sql string }{{"project", "projects", `INSERT INTO projects(company_id,customer_id,name) VALUES($1,$2,'Project') RETURNING id`}, {"job", "jobs", `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Job') RETURNING id`}, {"asset", "assets", fmt.Sprintf(`INSERT INTO assets(ownership_class,company_id,customer_id,name,asset_type_id) VALUES('customer_owned',$1,$2,'Asset',%d) RETURNING id`, aType.ID)}, {"estimate", "estimates", `INSERT INTO estimates(company_id,customer_id,title) VALUES($1,$2,'Estimate') RETURNING id`}, {"invoice", "invoices", `INSERT INTO invoices(company_id,customer_id,title,invoice_number,status_id) VALUES($1,$2,'Invoice',45001,(SELECT s.id FROM statuses s JOIN status_workflows w ON w.id=s.workflow_id WHERE w.object_type='invoice' AND lower(s.name)='void' LIMIT 1)) RETURNING id`}}
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "comment.add", Scope: authorization.All}, {Permission: "comment.remove", Scope: authorization.Own}}
	for _, target := range targets {
		grants = append(grants, authorization.Grant{Permission: target.kind + ".view", Scope: authorization.All})
	}
	role, err := auth.SaveRole(ctx, actor, 0, "Comment all parents", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("matrix@test").SetName("Matrix author").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	for _, target := range targets {
		var id int64
		if err := pool.QueryRow(ctx, target.sql, company, c.ID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		path := fmt.Sprintf("/%s/%d/comments", target.path, id)
		commentPost(t, router, cookie, path, url.Values{"content": {target.kind + " contribution"}}, 200)
		var commentID int64
		if err := pool.QueryRow(ctx, `SELECT id FROM comments WHERE object_type=$1 AND object_id=$2`, target.kind, id).Scan(&commentID); err != nil {
			t.Fatal(err)
		}
		expectStatus(t, router, cookie, "GET", fmt.Sprintf("%s/%d/audit", path, commentID), 403)
		commentPost(t, router, cookie, fmt.Sprintf("%s/%d/delete", path, commentID), nil, 200)
		if _, err := pool.Exec(ctx, fmt.Sprintf(`UPDATE %s SET deleted_at=clock_timestamp() WHERE id=$1`, target.path), id); err != nil {
			t.Fatal(err)
		}
		expectStatus(t, router, cookie, "GET", path, 404)
		commentPost(t, router, cookie, path, url.Values{"content": {"archived"}}, 404)
	}
	// Remove Own remains independently useful without Add or parent Edit.
	path := fmt.Sprintf("/customers/%d/comments", c.ID)
	commentPost(t, router, cookie, path, url.Values{"content": {"own remove"}}, 200)
	var own int64
	if err := pool.QueryRow(ctx, `SELECT id FROM comments WHERE content='own remove'`).Scan(&own); err != nil {
		t.Fatal(err)
	}
	removeRole, err := auth.SaveRole(ctx, actor, 0, "Remove only", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "comment.remove", Scope: authorization.Own}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, actor, u.ID, removeRole); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path, 200)
	if strings.Contains(body, "Add Comment") || !strings.Contains(body, "Remove Comment") {
		t.Fatal("independent remove controls missing")
	}
	commentPost(t, router, cookie, path, url.Values{"content": {"no Add grant"}}, 403)
	commentPost(t, router, cookie, fmt.Sprintf("%s/%d/delete", path, own), nil, 200)
	// Financial-only is not operational parent access, even with the legacy admin label.
	financial, err := auth.SaveRole(ctx, actor, 0, "Financial only", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'customer.financial.view','A')`, financial); err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, actor, u.ID, financial); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path, 404)
	for _, parent := range []string{"contacts", "locations", "items", "time-entries"} {
		expectStatus(t, router, sessionCookie(t, ctx, sessions, admin), "GET", "/"+parent+"/1/comments", 404)
	}
}

func TestCommentsRecheckGrantWhileWaitingToCommitHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "comment.add", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Revocable Comment", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("revocable-comment@test").SetName("Revocable").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Commit check").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", fmt.Sprintf("/customers/%d/comments", c.ID), strings.NewReader(url.Values{"content": {"revoked before commit"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=190019 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request did not reach commit-time authorization lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='comment.add'`, role); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	response := <-done
	if response.Code != 403 {
		t.Fatalf("revoked request: %d %s", response.Code, response.Body.String())
	}
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/comments", c.ID), 200)
	if strings.Contains(body, "revoked before commit") {
		t.Fatal("revoked mutation published")
	}
}

func TestCommentsDoNotFollowConvertedParentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Conversion parent").SaveX(ctx)
	var estimate int64
	if err := pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title,status_id) VALUES($1,$2,'Original identity',(SELECT s.id FROM statuses s JOIN status_workflows w ON s.workflow_id=w.id WHERE w.object_type='estimate' AND s.category_key='estimate:accepted' LIMIT 1)) RETURNING id`, company, c.ID).Scan(&estimate); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/estimates/%d/comments", estimate)
	commentPost(t, router, cookie, path, url.Values{"content": {"thread travels"}}, 200)
	var top int64
	if err := pool.QueryRow(ctx, `SELECT id FROM comments WHERE content='thread travels'`).Scan(&top); err != nil {
		t.Fatal(err)
	}
	commentPost(t, router, cookie, path, url.Values{"content": {"reply travels"}, "reply_to": {fmt.Sprint(top)}}, 200)
	commentPost(t, router, cookie, fmt.Sprintf("/estimates/%d/convert", estimate), url.Values{"idempotency_key": {"04500000-0000-4000-8000-000000000050"}}, 303)
	commentPost(t, router, cookie, path, url.Values{"content": {"stale original"}, "reply_to": {fmt.Sprint(top)}}, 404)
	expectStatus(t, router, cookie, "GET", path, 404)
	var invoice int64
	if err := pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1 AND reverted_at IS NULL`, estimate).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/invoices/%d/comments", invoice), 200)
	if !strings.Contains(body, "thread travels") || !strings.Contains(body, "reply travels") || strings.Contains(body, "stale original") {
		t.Fatal("conversion changed or retargeted Comment intent")
	}
}

func TestCommentsAPICreateOnceAndOriginalThread(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API parent").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("API comments").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Comment reference harness")
	if err != nil {
		t.Fatal(err)
	}
	router := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d/comments", j.ID)
	parentRevision := commentRevisionHTTP(t, router, token, "job", j.ID)
	post := func(path, body string, want int) map[string]any {
		t.Helper()
		var staged map[string]any
		if err := json.Unmarshal([]byte(body), &staged); err != nil {
			t.Fatal(err)
		}
		staged["parent_revision"] = parentRevision // captured once, never refreshed on replay
		raw, _ := json.Marshal(staged)
		body = string(raw)
		r := httptest.NewRequest("POST", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("API %s: %d want %d %s", path, w.Code, want, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	body := `{"operation_id":"04500000-0000-4000-8000-000000000001","content":"offline original"}`
	first := post(path, body, 200)["data"].(map[string]any)
	var deviceRecorded bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit a JOIN sessions s ON (a.detail->>'session_id')::bigint=s.id WHERE a.action='comment.add' AND a.detail->>'operation_id'='04500000-0000-4000-8000-000000000001' AND s.device_name='Comment reference harness')`).Scan(&deviceRecorded); err != nil || !deviceRecorded {
		t.Fatalf("missing API device attribution: %v", err)
	}
	client.Job.UpdateOneID(j.ID).SetJobType("Unrelated edit").SaveX(ctx)
	replay := post(path, body, 200)["data"].(map[string]any)
	if first["comment_id"] != replay["comment_id"] {
		t.Fatal("replay created a second effect")
	}
	post(path, `{"operation_id":"04500000-0000-4000-8000-000000000001","content":"changed intent"}`, 409)
	id := int64(first["comment_id"].(float64))
	remove := fmt.Sprintf("%s/%d/remove", path, id)
	post(remove, `{"operation_id":"04500000-0000-4000-8000-000000000002"}`, 200)
	post(remove, `{"operation_id":"04500000-0000-4000-8000-000000000003"}`, 200)
	post(path, fmt.Sprintf(`{"operation_id":"04500000-0000-4000-8000-000000000004","content":"closed","reply_to":%d}`, id), 409)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM comments WHERE object_type='job' AND object_id=$1`, j.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("create once count=%d err=%v", count, err)
	}
}

func TestCommentsAssignedScopeAndLostResponseHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Assigned collaboration", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}, {Permission: "comment.add", Scope: authorization.Assigned}, {Permission: "comment.remove", Scope: authorization.Own}, {Permission: "customer.view", Scope: authorization.Context}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("assigned-comment@test").SetName("Assigned").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Restricted master").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned parent").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/jobs/%d/comments", j.ID)
	commentPost(t, web, cookie, path, url.Values{"content": {"allowed independently"}}, 200)
	expectStatus(t, web, cookie, "GET", fmt.Sprintf("/customers/%d/comments", c.ID), 404)
	token, err := sessions.CreateMobile(ctx, u.ID, "offline harness")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	parentRevision := commentRevisionHTTP(t, api, token, "job", j.ID)
	call := func(method, path, body string, want int) string {
		t.Helper()
		if method == "POST" {
			var staged map[string]any
			if err := json.Unmarshal([]byte(body), &staged); err != nil {
				t.Fatal(err)
			}
			staged["parent_revision"] = parentRevision // keep original eligibility after scope loss
			raw, _ := json.Marshal(staged)
			body = string(raw)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: %d want %d %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	const op = "04500000-0000-4000-8000-000000000010"
	body := `{"operation_id":"` + op + `","content":"sensitive lost response"}`
	call("POST", path, body, 200)
	// Narrow View to Assigned and remove the assignment. Own removal cannot bypass it.
	_, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Assigned collaboration", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "comment.add", Scope: authorization.Assigned}, {Permission: "comment.remove", Scope: authorization.Own}}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	call("GET", path, "", 404)
	receipt := call("GET", "/comment-operations/"+op, "", 200)
	if strings.Contains(receipt, "sensitive") || !strings.Contains(receipt, "accepted") {
		t.Fatalf("unsafe receipt: %s", receipt)
	}
	call("POST", path, body, 200)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM comments WHERE content='sensitive lost response'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	call("POST", fmt.Sprintf("%s/%d/remove", path, id), `{"operation_id":"04500000-0000-4000-8000-000000000011"}`, 404)
	commentPost(t, web, cookie, fmt.Sprintf("%s/%d/delete", path, id), nil, 404)
}

func TestCommentsIndependentGrantsAndCharacterBoundaryHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Comment author", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "comment.add", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("comments@test").SetName("Comment author").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Comment parent").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/customers/%d/comments", c.ID)
	text := strings.Repeat("界", 9998) + "\nX"
	body := commentPost(t, router, cookie, path, url.Values{"content": {text}}, 200)
	if !strings.Contains(body, text) {
		t.Fatal("exact boundary or line breaks missing")
	}
	commentPost(t, router, cookie, path, url.Values{"content": {text + "X"}}, 400)
	body = requestBody(t, router, cookie, "GET", path, 200)
	if strings.Count(body, "comments-list-item") != 1 {
		t.Fatal("invalid text published")
	}
	feed := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/activity", c.ID), 200)
	if !strings.Contains(feed, "a comment on") || !strings.Contains(feed, "Comment author") {
		t.Fatal("ordinary attributable Comment activity missing")
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET name='Later display name' WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	feed = requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/activity", c.ID), 200)
	if !strings.Contains(feed, "<strong>Comment author</strong>") {
		t.Fatal("Comment event author was relabelled by a later identity edit")
	}
}

func TestCommentsRepliesRemovalAndRetainedTextHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Threads").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/customers/%d/comments", c.ID)
	commentPost(t, router, cookie, path, url.Values{"content": {"original secret"}}, 200)
	var top int64
	if err := pool.QueryRow(ctx, `SELECT id FROM comments WHERE content='original secret'`).Scan(&top); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,'comment_added','customer',$3,'{"comment_preview":"original secret"}')`, company, admin, c.ID); err != nil {
		t.Fatal(err)
	}
	commentPost(t, router, cookie, path, url.Values{"content": {"reply survives"}, "reply_to": {fmt.Sprint(top)}}, 200)
	var reply int64
	if err := pool.QueryRow(ctx, `SELECT id FROM comments WHERE content='reply survives'`).Scan(&reply); err != nil {
		t.Fatal(err)
	}
	commentPost(t, router, cookie, path, url.Values{"content": {"nested forbidden"}, "reply_to": {fmt.Sprint(reply)}}, 409)
	commentPost(t, router, cookie, fmt.Sprintf("%s/%d/delete", path, reply), nil, 200)
	commentPost(t, router, cookie, path, url.Values{"content": {"second reply"}, "reply_to": {fmt.Sprint(top)}}, 200)
	body := commentPost(t, router, cookie, fmt.Sprintf("%s/%d/delete", path, top), nil, 200)
	if strings.Contains(body, "original secret") || strings.Contains(body, "reply survives") || !strings.Contains(body, "second reply") || !strings.Contains(body, "Removed by Admin") {
		t.Fatalf("bad removal projection: %s", body)
	}
	commentPost(t, router, cookie, path, url.Values{"content": {"closed forbidden"}, "reply_to": {fmt.Sprint(top)}}, 409)
	commentPost(t, router, cookie, fmt.Sprintf("%s/%d/delete", path, top), nil, 200)
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("%s/%d/audit", path, top), 200)
	if !strings.Contains(body, "original secret") {
		t.Fatal("audit text missing")
	}
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/activity", c.ID), 200)
	if strings.Contains(body, "original secret") {
		t.Fatal("removed text leaked through historical feed preview")
	}
}
