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
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestWorkflowIndependentGrantsHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Workflow designer", []authorization.Grant{{Permission: "configuration.workflow.view", Scope: authorization.Configuration}, {Permission: "configuration.workflow.manage", Scope: authorization.Configuration}})
	if err != nil {
		t.Fatal(err)
	}
	var user int64
	if err = pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,'designer@test','Designer','x','tech',$2) RETURNING id`, company, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user)
	expectStatus(t, router, cookie, "GET", "/settings/statuses/job", 200)
	var workflow int64
	if err = pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Jobs','job') RETURNING id`, company).Scan(&workflow); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) SELECT $1,$2,label,'job:'||key,1,true FROM (VALUES ('new','New'),('travel_time','Travel'),('in_progress','Working'),('pending','Pending'),('completed','Completed'),('canceled','Canceled')) v(key,label)`, company, workflow); err != nil {
		t.Fatal(err)
	}
	post := func(name string, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/settings/statuses/job", strings.NewReader(url.Values{"name": {name}, "category": {"job:pending"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("create = %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	post("Awaiting permit", http.StatusSeeOther)
	if os.Getenv("BROWSER_TESTS") == "1" {
		server := httptest.NewServer(router)
		defer server.Close()
		cmd := exec.Command("node", "testdata/browser/workflow-settings.cjs", server.URL, cookie.Value)
		cmd.Env = os.Environ()
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("browser: %v\n%s", err, output)
		}
	}
	// Failure of the immutable ledger must also roll back the definition and
	// ordinary activity projection.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_workflow_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='configuration.workflow.manage' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_workflow_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_workflow_audit()`); err != nil {
		t.Fatal(err)
	}
	post("Must roll back", http.StatusUnprocessableEntity)
	body := requestBody(t, router, cookie, "GET", "/settings/statuses/job", 200)
	if strings.Contains(body, "Must roll back") {
		t.Fatal("audit failure retained definition")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_workflow_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Workflow reader", []authorization.Grant{{Permission: "configuration.workflow.view", Scope: authorization.Configuration}}, 1); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", "/settings/statuses/job", 200)
	post("Denied", http.StatusForbidden)
	if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Empty", nil, 2); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", "/settings/statuses/job", 403)
}

func TestWorkflowIntentProductionAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	var initial int64
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:new' LIMIT 1`, company).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Intent customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Intent job").SetStatusID(initial).SaveX(ctx)
	svc := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	target, err := svc.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Job, Name: "API target", Category: statusflow.JobPending})
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Workflow API")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, want int) string {
		t.Helper()
		if method == "POST" && strings.HasSuffix(path, "/status") {
			body = newExecutionWireIntent(body)
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	path := fmt.Sprintf("/jobs/%d/status", job.ID)
	call("POST", path, fmt.Sprintf(`{"status_id":%d}`, target.ID), 409)
	choices := call("GET", path+"-intents", "", 200)
	var envelope struct {
		Data []struct {
			Intent statusflow.Intent `json:"intent"`
		} `json:"data"`
	}
	if err = json.Unmarshal([]byte(choices), &envelope); err != nil {
		t.Fatal(err)
	}
	var intent statusflow.Intent
	for _, choice := range envelope.Data {
		if choice.Intent.StatusID == target.ID {
			intent = choice.Intent
		}
	}
	if intent.Revision == "" {
		t.Fatal("no usable target intent")
	}
	if _, err = svc.Update(ctx, actor, target.ID, "API renamed", "#112233"); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(intent)
	call("POST", path, string(data), 200)
	if err = svc.TransitionJob(ctx, actor, job.ID, initial); err != nil {
		t.Fatal(err)
	}
	response := call("POST", path, string(data), 409)
	if !strings.Contains(response, "status_reselection_required") {
		t.Fatal(response)
	}
	call("POST", "/settings/statuses/job", `{}`, 404)
}

func TestWorkflowCategoryMoveUpdatesProjectProgressProjection(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	status, err := svc.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Project, Name: "Review gate", Category: statusflow.ProjectPending})
	if err != nil {
		t.Fatal(err)
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Progress customer").SaveX(ctx)
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName("Progress project").SetStatusID(status.ID).SetCompletionPercentage(37).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/projects/%d", project.ID)
	if err = svc.Move(ctx, actor, statusflow.MoveRequest{StatusID: status.ID, Category: statusflow.ProjectCompleted, ConfirmInUse: true}); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", path, 200)
	if !strings.Contains(body, `<progress value="100"`) {
		t.Fatal("Completed category did not project 100 percent")
	}
	if err = svc.Move(ctx, actor, statusflow.MoveRequest{StatusID: status.ID, Category: statusflow.ProjectPending, ConfirmInUse: true}); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", path, 200)
	if !strings.Contains(body, `<progress value="37"`) {
		t.Fatal("leaving Completed lost stored progress")
	}
}
