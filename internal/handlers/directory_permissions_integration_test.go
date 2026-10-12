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
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/justinas/nosurf"
	"time"
)

func TestDirectoryIsOperationalOnlyThroughProductionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "People viewer", []authorization.Grant{{Permission: "directory.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Operational person").SetEmail("person@test").SetPasswordHash("secret hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	r := httptest.NewRequest("GET", "/directory", nil)
	r.AddCookie(cookie)
	r.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("directory: %d %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("directory rows: %v", rows)
	}
	for _, row := range rows {
		for key := range row {
			if key != "id" && key != "name" && key != "email" && key != "available" {
				t.Fatalf("security facet leaked: %s", key)
			}
		}
	}
	body := requestBody(t, router, cookie, "GET", "/directory", 200)
	if strings.Contains(body, fmt.Sprintf("href=\"/users/%d", u.ID)) {
		t.Fatal("directory links to sealed account")
	}
	for _, path := range []string{"/users", fmt.Sprintf("/users/%d", u.ID), "/settings/authorization"} {
		expectStatus(t, router, cookie, "GET", path, 403)
	}
	for _, path := range []string{fmt.Sprintf("/users/%d?role=admin", u.ID), "/settings/authorization/assign", "/settings/authorization/roles"} {
		expectStatus(t, router, cookie, "POST", path, 403)
	}
	if err := svc.SetEnabled(ctx, authorization.Actor{UserID: admin}, u.ID, false); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", "/directory", 303)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	body = requestBody(t, router, adminCookie, "GET", "/directory", 200)
	assertContains(t, body, "Unavailable")
}

func TestRolesAndDirectoryPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetEmail("browser-person@test").SetName("Browser person").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	personCookie := sessionCookie(t, ctx, sessions, u.ID)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	cmdCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	output, err := exec.CommandContext(cmdCtx, "node", "testdata/browser/roles-directory.cjs", server.URL, adminCookie.Value, personCookie.Value, fmt.Sprint(u.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	// Fresh router and connections read the same persisted identity and grants.
	restarted := New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()})
	body := requestBody(t, restarted, adminCookie, "GET", fmt.Sprintf("/users/%d", u.ID), 200)
	assertContains(t, body, "Browser renamed person")
	assertContains(t, body, "Browser renamed Role")
}

func accountPost(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values, want int) {
	t.Helper()
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("POST %s: %d want %d: %s", path, w.Code, want, w.Body.String())
	}
}

func TestSealedIdentityAndCredentialActionsThroughProductionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Before edit").SetEmail("before@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	userCookie := sessionCookie(t, ctx, sessions, u.ID)
	mobile, err := sessions.CreateMobile(ctx, u.ID, "Account security test")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for _, path := range []string{"/users", "/settings/authorization", "/directory"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+mobile)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("field API exposed administrative/directory surface %s: %d", path, w.Code)
		}
	}
	path := fmt.Sprintf("/users/%d", u.ID)
	accountPost(t, router, adminCookie, path, url.Values{"name": {"After edit"}, "email": {"after@test"}, "role": {"admin"}}, 409)
	accountPost(t, router, adminCookie, path, url.Values{"name": {"After edit"}, "email": {"after@test"}}, 303)
	body := requestBody(t, router, adminCookie, "GET", path, 200)
	assertContains(t, body, "after@test")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_id=$1 AND action='user.identity.edit'`, u.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("identity audit=%d: %v", count, err)
	}
	accountPost(t, router, userCookie, path+"/require-change", nil, 403)
	accountPost(t, router, adminCookie, path+"/require-change", nil, 303)
	expectStatus(t, router, userCookie, "GET", "/preferences", 303)
	r := httptest.NewRequest("GET", "/me", nil)
	r.Header.Set("Authorization", "Bearer "+mobile)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("mobile session survived require-change: %d %s", w.Code, w.Body.String())
	}
	var required bool
	if err := pool.QueryRow(ctx, `SELECT force_password_change FROM users WHERE id=$1`, u.ID).Scan(&required); err != nil || !required {
		t.Fatalf("required=%v: %v", required, err)
	}
	accountPost(t, router, adminCookie, path+"/revoke-sessions", nil, 303)
	// Audit storage failure must roll back the identity change.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_account_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_account_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_account_audit()`); err != nil {
		t.Fatal(err)
	}
	accountPost(t, router, adminCookie, path, url.Values{"name": {"Must roll back"}, "email": {"rollback@test"}}, 409)
	body = requestBody(t, router, adminCookie, "GET", path, 200)
	assertContains(t, body, "after@test")
	if strings.Contains(body, "rollback@test") {
		t.Fatal("audit failure committed identity")
	}
}

func TestRoleEditorUsesClosedCatalogThroughProductionHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/settings/authorization", 200)
	assertContains(t, body, `value="directory.view:A"`)
	assertContains(t, body, `value="customer.view:C"`)
	if strings.Contains(body, `<textarea name="grants"`) || strings.Contains(body, `value="administration:S"`) || strings.Contains(body, `value="cost.view:J"`) {
		t.Fatal("editor exposes arbitrary or sealed/invalid grants")
	}
	accountPost(t, router, cookie, "/settings/authorization/roles", url.Values{"name": {"Directory only"}, "catalog_form": {"true"}, "grant": {"directory.view:A"}}, 303)
	accountPost(t, router, cookie, "/settings/authorization/roles", url.Values{"name": {"Invalid scope"}, "catalog_form": {"true"}, "grant": {"directory.view:J"}}, 409)
	accountPost(t, router, cookie, "/settings/authorization/roles", url.Values{"name": {"Missing view"}, "catalog_form": {"true"}, "grant": {"customer.edit:A"}}, 409)
	accountPost(t, router, cookie, "/settings/authorization/roles", url.Values{"name": {"Empty allowed"}, "catalog_form": {"true"}}, 303)
}

func TestSealedUserCreationChoosesExplicitRoleAndResetRevokesSessions(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	values := url.Values{"name": {"New operator"}, "email": {"new@example.com"}, "password": {"StrongPassword1!"}, "confirm_password": {"StrongPassword1!"}, "role_id": {"2"}}
	accountPost(t, router, cookie, "/users", values, 303)
	var id, role int64
	var force bool
	if err := pool.QueryRow(ctx, `SELECT id,authorization_role_id,force_password_change FROM users WHERE email='new@example.com'`).Scan(&id, &role, &force); err != nil || role != 2 || !force {
		t.Fatalf("created role=%d force=%v: %v", role, force, err)
	}
	// Test normal sessions before a sealed reset.
	if _, err := pool.Exec(ctx, `UPDATE users SET force_password_change=false WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	other := sessionCookie(t, ctx, sessions, id)
	accountPost(t, router, cookie, fmt.Sprintf("/users/%d/reset-password", id), url.Values{"password": {"ReplacementPassword1!"}, "confirm_password": {"ReplacementPassword1!"}}, 303)
	expectStatus(t, router, other, "GET", "/preferences", 303)
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_id=$1 AND action IN ('user.create','user.password.require_change')`, id).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("create/reset audit=%d: %v", auditCount, err)
	}
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/users/%d", id), 200)
	assertContains(t, body, "Dispatcher")
}

func TestDisabledDirectoryPersonCannotReceiveAssignmentAtCommit(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Assignee").SetEmail("assign@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	c := client.Customer.Create().SetCompanyID(company).SetFirstName("Customer").SetLastName("Fixture").SetDisplayName("Assignment customer").SaveX(ctx)
	jobs := services.NewJobService(client)
	job, err := jobs.Create(ctx, company, services.JobCreateParams{CustomerID: c.ID, BillingType: "flat_rate", CustomFields: "{}", JobType: "Retained assignment", Assignments: []services.JobAssignment{{UserID: u.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := authorization.New(pool).SetEnabled(ctx, authorization.Actor{UserID: admin}, u.ID, false); err != nil {
		t.Fatal(err)
	}
	retained := []services.JobAssignment{{UserID: u.ID}}
	if _, err := jobs.Update(ctx, company, job.ID, services.JobUpdateParams{Assignments: &retained}); err != nil {
		t.Fatalf("retained disabled assignment: %v", err)
	}
	if _, err := jobs.Create(ctx, company, services.JobCreateParams{CustomerID: c.ID, BillingType: "flat_rate", CustomFields: "{}", JobType: "Rejected disabled assignment", Assignments: retained}); err == nil {
		t.Fatal("new assignment accepted disabled user")
	}
	unassigned := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Move must roll back").SaveX(ctx)
	if _, err := jobs.Move(ctx, unassigned.ID, u.ID, time.Now(), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("move accepted disabled user")
	}
	if moved, err := jobs.GetByID(ctx, unassigned.ID); err != nil || moved.StartTime != nil {
		t.Fatalf("failed move left schedule effect: %v %v", moved, err)
	}
	if err := authorization.New(pool).SetEnabled(ctx, authorization.Actor{UserID: admin}, u.ID, true); err != nil {
		t.Fatal(err)
	}
	// Hold an uncommitted disable: planning saw an enabled person but its
	// assignment insert must wait and re-read availability after disable commits.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE users SET is_active=false WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := jobs.Create(ctx, company, services.JobCreateParams{CustomerID: c.ID, BillingType: "flat_rate", CustomFields: "{}", JobType: "Racing assignment", Assignments: retained})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("planning did not wait for disable: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("racing disable accepted new assignment")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("assignment deadlock")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE job_type IN ('Racing assignment','Rejected disabled assignment')`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial job effects=%d: %v", count, err)
	}
	// DB guard covers direct assignment writers too, not just form validation.
	_, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, job.ID, u.ID)
	if err == nil {
		t.Fatal("direct disabled assignment accepted")
	}
}

func TestPlanningSelectionRechecksDirectoryGrantAfterWaiting(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Planner directory", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "directory.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	planner := client.User.Create().SetCompanyID(company).SetName("Planner").SetEmail("planner@test").SetPasswordHash("hash").SetRole("dispatcher").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, planner.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Planning customer").SaveX(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='directory.view'`, role); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := services.NewJobService(client).Create(authorization.WithActor(ctx, svc, authorization.Actor{UserID: planner.ID}), company, services.JobCreateParams{CustomerID: c.ID, JobType: "Revoked directory selection", BillingType: "flat_rate", CustomFields: "{}", Assignments: []services.JobAssignment{{UserID: admin}}})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("selection did not wait: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("selection accepted revoked directory grant")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("selection deadlock")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE job_type='Revoked directory selection'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial job effect=%d: %v", count, err)
	}
}

func TestSealedInvitationRenewalAuditsAndRechecksAuthority(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	invites := services.NewInvitationService(client)
	u := client.User.Create().SetCompanyID(company).SetName("Pending invitation").SetEmail("pending@test").SetPasswordHash("hash").SetRole("tech").SetIsActive(false).SetWelcomeEmailSentAt(time.Now()).SaveX(ctx)
	actorCtx := authorization.WithActor(ctx, svc, authorization.Actor{UserID: admin})
	_, token, err := invites.RenewPendingInvite(actorCtx, company, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invites.ValidateInvite(ctx, token); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='user.welcome.resend' AND target_id=$1`, u.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("renewal audit=%d: %v", count, err)
	}
	viewer := client.User.Create().SetCompanyID(company).SetName("No sealed access").SetEmail("not-admin@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if _, _, err := invites.RenewPendingInvite(authorization.WithActor(ctx, svc, authorization.Actor{UserID: viewer.ID}), company, u.ID); err == nil {
		t.Fatal("unsealed renewal accepted")
	}
	if _, err := invites.ValidateInvite(ctx, token); err != nil {
		t.Fatalf("denied renewal invalidated old invitation: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_invite_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_invite_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_invite_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := invites.RenewPendingInvite(actorCtx, company, u.ID); err == nil {
		t.Fatal("audit failure accepted renewal")
	}
	if _, err := invites.ValidateInvite(ctx, token); err != nil {
		t.Fatalf("audit failure invalidated old invitation: %v", err)
	}
}

func TestConcurrentSealedSelfDisablePreservesAdministratorThroughHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	first := sessionCookie(t, ctx, sessions, admin)
	accountPost(t, router, first, fmt.Sprintf("/users/%d/disable", admin), nil, 409)
	var secondID int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,name,email,password_hash,role,authorization_role_id) SELECT $1,'Second Administrator','second-admin@test',password_hash,'tech',1 FROM users WHERE id=$2 RETURNING id`, company, admin).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	second := sessionCookie(t, ctx, sessions, secondID)
	start := make(chan struct{})
	results := make(chan int, 2)
	for _, target := range []struct {
		id     int64
		cookie *http.Cookie
	}{{admin, first}, {secondID, second}} {
		go func(id int64, cookie *http.Cookie) {
			<-start
			r := httptest.NewRequest("POST", fmt.Sprintf("/users/%d/disable", id), nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			results <- w.Code
		}(target.id, target.cookie)
	}
	close(start)
	a, b := <-results, <-results
	if !(a == 303 && b == 409 || a == 409 && b == 303) {
		t.Fatalf("self-disable results %d,%d", a, b)
	}
	var enabled int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users u JOIN authorization_roles r ON r.id=u.authorization_role_id WHERE r.protected AND u.is_active`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("remaining administrators=%d: %v", enabled, err)
	}
}

func TestSealedRecoveryResendReplacesLinkAtomically(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Recovery target").SetEmail("recovery@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	accountPost(t, router, cookie, fmt.Sprintf("/users/%d/resend-recovery", u.ID), nil, 403)
	resets := services.NewPasswordResetService(client)
	actorCtx := authorization.WithActor(ctx, authorization.New(pool), authorization.Actor{UserID: admin})
	first, err := resets.CreateToken(actorCtx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resets.CreateToken(actorCtx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resets.Validate(ctx, first); err == nil {
		t.Fatal("replaced recovery link still usable")
	}
	if id, err := resets.Validate(ctx, second); err != nil || id != u.ID {
		t.Fatalf("latest recovery target=%d: %v", id, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='user.recovery.resend' AND target_id=$1`, u.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("recovery audit=%d: %v", count, err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_recovery_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_recovery_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := resets.CreateToken(actorCtx, u.ID); err == nil {
		t.Fatal("audit failure accepted recovery replacement")
	}
	if _, err := resets.Validate(ctx, second); err != nil {
		t.Fatalf("audit failure invalidated old recovery link: %v", err)
	}
}
