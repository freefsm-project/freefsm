package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/ent/customer"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/justinas/nosurf"
	"golang.org/x/crypto/bcrypt"
)

func permissionHTTPFixture(t *testing.T, legacy ...bool) (*pgxpool.Pool, *ent.Client, http.Handler, *services.SessionService, int64, int64) {
	return permissionHTTPFixtureSeeded(t, nil, legacy...)
}

// A seed callback installs recorded pre-configuration facts after migration 057
// and before forward migrations, while retaining the production HTTP fixture.
func permissionHTTPFixtureSeeded(t *testing.T, seed func(*pgxpool.Pool), legacy ...bool) (*pgxpool.Pool, *ent.Client, http.Handler, *services.SessionService, int64, int64) {
	return permissionHTTPFixtureConfigured(t, database.MigrationFS(), seed, legacy...)
}

func permissionHTTPFixtureMigrations(t *testing.T, migrations fs.FS, legacy ...bool) (*pgxpool.Pool, *ent.Client, http.Handler, *services.SessionService, int64, int64) {
	return permissionHTTPFixtureConfigured(t, migrations, nil, legacy...)
}

func permissionHTTPFixtureConfigured(t *testing.T, migrations fs.FS, seed func(*pgxpool.Pool), legacy ...bool) (*pgxpool.Pool, *ent.Client, http.Handler, *services.SessionService, int64, int64) {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("permission_http_%d", time.Now().UnixNano())
	if _, err := root.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	scoped, err := dsnWithSearchPath(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig, err := pgxpool.ParseConfig(scoped)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	if (len(legacy) > 0 && legacy[0]) || seed != nil {
		through := "054_backup_activity.up.sql"
		if seed != nil {
			through = "057_protected_host_audit.up.sql"
		}
		old := fstest.MapFS{}
		entries, err := fs.ReadDir(migrations, ".")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() > through {
				continue
			}
			b, err := fs.ReadFile(migrations, e.Name())
			if err != nil {
				t.Fatal(err)
			}
			old[e.Name()] = &fstest.MapFile{Data: b}
		}
		migrations = old
	}
	if err := (&database.DB{Pool: pool}).Migrate(ctx, migrations); err != nil {
		t.Fatal(err)
	}
	raw := stdlib.OpenDB(*poolConfig.ConnConfig)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, raw)))
	t.Cleanup(func() {
		client.Close()
		pool.Close()
		root.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		root.Close()
	})
	var company, admin int64
	if err := pool.QueryRow(ctx, `SELECT id FROM companies LIMIT 1`).Scan(&company); err != nil {
		if err != pgx.ErrNoRows {
			t.Fatal(err)
		}
		// Pre-v1 upgrade fixtures precede the fresh-install bootstrap migration.
		if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Legacy fixture','legacy-fixture') RETURNING id`).Scan(&company); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE company_settings SET company_id=$1 WHERE company_id IS NULL`, company); err != nil {
			t.Fatal(err)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("PermissionFixture1!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if !(len(legacy) > 1 && legacy[1]) {
		if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'admin@test','Admin',$2,'admin') RETURNING id`, company, string(hash)).Scan(&admin); err != nil {
			t.Fatal(err)
		}
	}
	if seed != nil {
		seed(pool)
		if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
			t.Fatal(err)
		}
	}
	if len(legacy) > 0 && legacy[0] {
		if _, err := pool.Exec(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,'legacy@test','Legacy dispatcher','x','dispatcher')`, company); err != nil {
			t.Fatal(err)
		}
		if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
			t.Fatal(err)
		}
	}
	sessions := services.NewSessionService(pool)
	router := middleware.Company(services.NewCompanySettingsService(client))(New(pool, client, sessions, &config.Config{UploadDir: t.TempDir(), MaxUploadSize: 25 << 20}))
	return pool, client, router, sessions, company, admin
}

func TestAdministratorReviewedCutoverThroughProductionHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t, true)
	ctx := context.Background()
	login := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"email": {"admin@test"}, "password": {"PermissionFixture1!"}}.Encode()))
	login.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	loggedIn := httptest.NewRecorder()
	router.ServeHTTP(loggedIn, login)
	var adminCookie *http.Cookie
	for _, cookie := range loggedIn.Result().Cookies() {
		if cookie.Name == "session" {
			adminCookie = cookie
		}
	}
	if loggedIn.Code != 303 || adminCookie == nil {
		t.Fatalf("existing Administrator could not authenticate: %d %s", loggedIn.Code, loggedIn.Body.String())
	}
	var other int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE email='legacy@test'`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	otherCookie := sessionCookie(t, ctx, sessions, other)
	expectStatus(t, router, adminCookie, "GET", "/customers", 503)
	expectStatus(t, router, adminCookie, "GET", "/users", 403)
	body := requestBody(t, router, adminCookie, "GET", "/settings/authorization", 200)
	assertContains(t, body, "pending explicit Administrator review")
	post := func(cookie *http.Cookie, path string, values url.Values, want int) {
		t.Helper()
		if values.Get("review") == "true" {
			get := httptest.NewRequest("GET", "/settings/authorization", nil)
			get.AddCookie(cookie)
			get.Header.Set("Accept", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, get)
			var review authorization.Review
			if err := json.Unmarshal(response.Body.Bytes(), &review); err != nil {
				t.Fatal(err)
			}
			values.Set("snapshot", review.Snapshot)
		}
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("POST %s status=%d want=%d body=%s", path, w.Code, want, w.Body.String())
		}
	}
	post(otherCookie, "/settings/authorization/roles", url.Values{"name": {"Minted"}, "grants": {"[]"}}, 403)
	post(adminCookie, "/settings/authorization/roles", url.Values{"name": {"Malformed target"}, "grants": {"[]"}, "role_id": {"not-a-role"}}, 400)
	post(adminCookie, "/settings/authorization/cutover", url.Values{"confirmation": {"APPROVE CUTOVER"}}, 409)
	post(adminCookie, "/settings/authorization/assign", url.Values{"user_id": {fmt.Sprint(admin)}, "role_id": {"1"}, "review": {"true"}}, 303)
	post(adminCookie, "/settings/authorization/cutover", url.Values{"confirmation": {"APPROVE CUTOVER"}}, 409)
	// Existing Dispatcher receives explicitly reviewed empty grants, not defaults.
	post(adminCookie, "/settings/authorization/assign", url.Values{"user_id": {fmt.Sprint(other)}, "role_id": {"2"}, "review": {"true"}}, 303)
	post(adminCookie, "/settings/authorization/cutover", url.Values{"confirmation": {"APPROVE CUTOVER"}}, 303)
	expectStatus(t, router, adminCookie, "GET", "/customers", 200)
	expectStatus(t, router, otherCookie, "GET", "/customers", 403)
	expectStatus(t, router, otherCookie, "POST", fmt.Sprintf("/users/%d?role=admin", other), 403)
	// Reconstruct the router against the same database to prove durable cutover.
	var ready bool
	var grants int
	if err := pool.QueryRow(ctx, `SELECT ready FROM authorization_state`).Scan(&ready); err != nil || !ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_grants WHERE role_id=2`).Scan(&grants); err != nil || grants != 0 {
		t.Fatalf("upgrade auto-granted %d grants: %v", grants, err)
	}
	// Replace the Administrator explicitly, preserving both legacy values.
	// The old `admin` discriminator must not retain any sealed editor.
	if _, err := pool.Exec(ctx, `UPDATE users SET password_hash=(SELECT password_hash FROM users WHERE id=$1) WHERE id=$2`, admin, other); err != nil {
		t.Fatal(err)
	}
	svc := authorization.New(pool)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, other, 1); err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, admin, 4); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/users", "/settings/authorization", "/activity?type=instance"} {
		expectStatus(t, router, adminCookie, "GET", path, 403)
	}
	expectStatus(t, router, adminCookie, "POST", "/dashboard/widgets/company-default", 403)
	post(adminCookie, "/settings/authorization/roles", url.Values{"name": {"Cannot mint authority"}, "grants": {"[]"}}, 403)
	expectStatus(t, router, otherCookie, "GET", "/settings/authorization", 200)
	expectStatus(t, router, otherCookie, "GET", "/activity?type=instance", 200)
}

func TestCustomerPermissionsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser explicit grants", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.create", Scope: authorization.All}, {Permission: "customer.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("browser@test").SetPasswordHash("hash").SetName("Browser").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/customer-permissions.cjs", server.URL, cookie.Value, adminCookie.Value, fmt.Sprint(role)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	var notes string
	if err := pool.QueryRow(ctx, `SELECT count(*),max(service_notes) FROM customers WHERE display_name='Browser persisted edit'`).Scan(&count, &notes); err != nil || count != 1 || notes != "Browser service notes" {
		t.Fatalf("persisted customer count=%d notes=%q err=%v", count, notes, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='customer'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("atomic audit count=%d err=%v", count, err)
	}
}

func TestCustomerPermissionHTTPScopesAndAtomicMutation(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	viewer, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Renamed custom office", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.create", Scope: authorization.All}, {Permission: "customer.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("custom@test").SetName("Custom").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, viewer); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user.ID)
	req := httptest.NewRequest("POST", "/customers", strings.NewReader(url.Values{"operation_id": {newOperationKey()}, "display_name": {"Operational customer"}, "service_notes": {"Gate code"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 303 {
		t.Fatalf("create status=%d body=%s", rr.Code, rr.Body.String())
	}
	var id int64
	var auditCount int
	if err := pool.QueryRow(ctx, `SELECT id FROM customers WHERE display_name='Operational customer'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='customer' AND target_id=$1`, id).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("audit=%d err=%v", auditCount, err)
	}
	expectStatus(t, router, cookie, "GET", "/customers", 200)
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", id), 200)
	activity := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/activity", id), 200)
	assertContains(t, activity, "Operational customer")
	if _, err := pool.Exec(ctx, `UPDATE customers SET billing_city='BILLING_PROBE',notes='OPERATIONAL_INTERNAL' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	viewBody := requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/edit", id), 200)
	assertNotContains(t, viewBody, "BILLING_PROBE")
	scoped := authorization.WithActor(ctx, svc, authorization.Actor{UserID: user.ID})
	if _, err := client.Customer.Query().Select(customer.FieldBillingCity).Strings(scoped); err == nil {
		t.Fatal("selected-column projection bypassed financial facet denial")
	}
	note := "Allowed operational edit"
	written, err := svc.SaveCustomer(ctx, authorization.Actor{UserID: user.ID}, id, authorization.CustomerInput{ServiceNotes: &note})
	if err != nil || written.BillingCity != "" {
		t.Fatalf("mutation representation exposed denied financial facet: err=%v", err)
	}
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, viewer, "View only", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}}, 1); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/customers/%d?display_name=forbidden", id), 403)
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, viewer, "Empty", nil, 2); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", id), 404)
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/contacts", id), 404)
}

func TestCustomerContextHTTPIsClosedAndAssignmentBound(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	tech := client.User.Create().SetCompanyID(company).SetEmail("field@test").SetName("Field").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, tech.ID, 3); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Allowed name").SetServiceNotes("Allowed service note").SetNotes("SECRET_INTERNAL").SetBillingCity("SECRET_BILLING").SetCustomFields(`[{"secret":"SECRET_FIELD"}]`).SetAssignedTo(tech.ID).SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Explicit Job").SaveX(ctx)
	client.JobAssignment.Create().SetUserID(tech.ID).SetJobID(job.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, tech.ID)
	path := fmt.Sprintf("/jobs/%d/customer-context", job.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Allowed service note")
	assertNotContains(t, body, "SECRET_")
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, tech.ID, "Context test")
	if err != nil {
		t.Fatal(err)
	}
	apiRequest := httptest.NewRequest("GET", path, nil)
	apiRequest.Header.Set("Authorization", "Bearer "+token)
	apiResponse := httptest.NewRecorder()
	api.ServeHTTP(apiResponse, apiRequest)
	if apiResponse.Code != 200 || strings.Contains(apiResponse.Body.String(), "SECRET_") || !strings.Contains(apiResponse.Body.String(), "Allowed service note") {
		t.Fatalf("API context: %d %s", apiResponse.Code, apiResponse.Body.String())
	}
	office := client.User.Create().SetCompanyID(company).SetEmail("legacy-office@test").SetName("Legacy office").SetPasswordHash("hash").SetRole("dispatcher").SaveX(ctx)
	officeToken, err := sessions.CreateMobile(ctx, office.ID, "Projection test")
	if err != nil {
		t.Fatal(err)
	}
	projectionRequest := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d", job.ID), nil)
	projectionRequest.Header.Set("Authorization", "Bearer "+officeToken)
	projectionResponse := httptest.NewRecorder()
	api.ServeHTTP(projectionResponse, projectionRequest)
	if projectionResponse.Code != 404 || strings.Contains(projectionResponse.Body.String(), "Explicit Job") {
		t.Fatalf("legacy Role label acquired Job View: %d %s", projectionResponse.Code, projectionResponse.Body.String())
	}
	jobViewer, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Job-only office reader", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, office.ID, jobViewer); err != nil {
		t.Fatal(err)
	}
	projectionResponse = httptest.NewRecorder()
	api.ServeHTTP(projectionResponse, projectionRequest)
	if projectionResponse.Code != 200 || strings.Contains(projectionResponse.Body.String(), "customer_id") {
		t.Fatalf("legacy Job projection bypassed Customer scope: %d %s", projectionResponse.Code, projectionResponse.Body.String())
	}
	search := requestBody(t, router, cookie, "GET", "/search?q=Allowed", 200)
	assertNotContains(t, search, "Allowed name")
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 404)
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", path, 404)
	client.JobAssignment.Create().SetUserID(tech.ID).SetJobID(job.ID).SaveX(ctx)
	client.Job.UpdateOneID(job.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	expectStatus(t, router, cookie, "GET", path, 404)
	// Direct Customer assignment retained as a historical fact grants nothing.
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), 404)
}

func TestJobBoundDocumentsRetainAuthorizedCustomerInputWithoutMasterLeak(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	tech := client.User.Create().SetCompanyID(company).SetEmail("documents@test").SetName("Field").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, tech.ID, 3); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Assigned Customer").SetTaxExempt(true).SetBillingCity("BILLING_PROBE").SetNotes("INTERNAL_PROBE").SaveX(ctx)
	client.Customer.Create().SetCompanyID(company).SetDisplayName("SIBLING_PROBE").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Bound document Job").SetLineItems(`[{"title":"Work","quantity":1,"unit_price":100,"taxable":true}]`).SaveX(ctx)
	client.JobAssignment.Create().SetUserID(tech.ID).SetJobID(job.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, tech.ID)
	body := requestBody(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/create-invoice", job.ID), 200)
	assertContains(t, body, "Assigned Customer")
	assertNotContains(t, body, "BILLING_PROBE")
	assertNotContains(t, body, "SIBLING_PROBE")
	assertNotContains(t, body, "INTERNAL_PROBE")
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/create-estimate", job.ID), 303)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Bound invoice"}, "invoice_date": {"2026-10-11"}, "due_date": {"2026-10-12"}, "tax_rate": {"9"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100,"taxable":true}]`}}
	req := httptest.NewRequest("POST", fmt.Sprintf("/jobs/%d/invoices", job.ID), strings.NewReader(values.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	if response.Code != 303 {
		t.Fatalf("bound create: %d %s", response.Code, response.Body.String())
	}
	var exempt bool
	if err := pool.QueryRow(ctx, `SELECT tax_rate=0 FROM invoices WHERE title='Bound invoice'`).Scan(&exempt); err != nil || !exempt {
		t.Fatalf("authorized pricing effect lost Customer exemption: zero=%v err=%v", exempt, err)
	}
	var invoiceID, estimateID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices WHERE title='Bound invoice'`).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM estimates WHERE job_id=$1`, job.ID).Scan(&estimateID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fmt.Sprintf("/invoices/%d", invoiceID), fmt.Sprintf("/invoices/%d/edit", invoiceID), fmt.Sprintf("/estimates/%d", estimateID), fmt.Sprintf("/estimates/%d/edit", estimateID)} {
		body := requestBody(t, router, cookie, "GET", path, 200)
		assertContains(t, body, "Assigned Customer")
		assertNotContains(t, body, "BILLING_PROBE")
		assertNotContains(t, body, "SIBLING_PROBE")
		assertNotContains(t, body, "INTERNAL_PROBE")
	}
}
