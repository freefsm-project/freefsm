package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func sidebarHTML(t *testing.T, body string) string {
	t.Helper()
	_, sidebar, ok := strings.Cut(body, `<aside class="sidebar"`)
	if !ok {
		t.Fatal("missing sidebar")
	}
	sidebar, _, ok = strings.Cut(sidebar, `</aside>`)
	if !ok {
		t.Fatal("unclosed sidebar")
	}
	return sidebar
}

func TestNavigationPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	workflow := []authorization.Grant{{Permission: "configuration.workflow.view", Scope: authorization.Configuration}, {Permission: "configuration.workflow.manage", Scope: authorization.Configuration}}
	workflowRole, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser workflow only", workflow)
	if err != nil {
		t.Fatal(err)
	}
	user := client.User.Create().SetCompanyID(company).SetEmail("nav-workflow-browser@test").SetName("Workflow only").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, workflowRole); err != nil {
		t.Fatal(err)
	}
	grants := append([]authorization.Grant{}, workflow...)
	for _, domain := range []string{"customer", "job", "project", "asset", "estimate", "invoice", "item"} {
		grants = append(grants, authorization.Grant{Permission: domain + ".view", Scope: authorization.All})
	}
	grants = append(grants, authorization.Grant{Permission: "time.view", Scope: authorization.Own})
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser navigator", grants)
	if err != nil {
		t.Fatal(err)
	}
	reader := client.User.Create().SetCompanyID(company).SetEmail("nav-browser@test").SetName("Navigator").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, reader.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customers(company_id,display_name) SELECT $1,'Navigation return '||lpad(i::text,3,'0') FROM generate_series(1,80) i`, company); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/browser/navigation.cjs", server.URL, sessionCookie(t, ctx, sessions, user.ID).Value, sessionCookie(t, ctx, sessions, reader.ID).Value, sessionCookie(t, ctx, sessions, admin).Value, fmt.Sprint(role), t.TempDir())
	output, err := cmd.CombinedOutput()
	t.Logf("navigation browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNavigationCollectionsFilterBeforePaginationHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("UNDISCLOSED CUSTOMER").SaveX(ctx)
	now := time.Now().UTC()
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Visible navigation Job").SetStartTime(now).SaveX(ctx)
	estimate := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Visible navigation Estimate").SaveX(ctx)
	invoice := client.Invoice.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Visible navigation Invoice").SetInvoiceNumber(1).SetInvoiceDate(now).SetDueDate(now).SaveX(ctx)
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetName("Visible navigation Project").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Machine").SaveX(ctx)
	client.Asset.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetAssetTypeID(typ.ID).SetName("Visible navigation Asset").SaveX(ctx)
	for i := range 27 {
		j := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType(fmt.Sprintf("HIDDEN navigation Job %d", i)).SetStartTime(now).SaveX(ctx)
		client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(j.ID).SetTitle("HIDDEN navigation Estimate").SaveX(ctx)
		client.Invoice.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(j.ID).SetTitle("HIDDEN navigation Invoice").SetInvoiceNumber(int64(i + 2)).SetInvoiceDate(now).SetDueDate(now).SaveX(ctx)
	}
	var foreign int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Foreign navigation','foreign-navigation') RETURNING id`).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	foreignCustomer := client.Customer.Create().SetCompanyID(foreign).SetDisplayName("FOREIGN CUSTOMER").SaveX(ctx)
	client.Job.Create().SetCompanyID(foreign).SetCustomerID(foreignCustomer.ID).SetJobType("FOREIGN JOB").SetStartTime(now).SaveX(ctx)
	client.Project.Create().SetCompanyID(foreign).SetCustomerID(foreignCustomer.ID).SetName("FOREIGN PROJECT").SaveX(ctx)
	var users []int64
	grants := []authorization.Grant{
		{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "estimate.view", Scope: authorization.Assigned}, {Permission: "invoice.view", Scope: authorization.Assigned},
		{Permission: "project.view", Scope: authorization.All}, {Permission: "asset.view", Scope: authorization.All}, {Permission: "time.view", Scope: authorization.Assigned},
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Navigation collection readers", grants)
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []string{"tech", "dispatcher"} {
		u := client.User.Create().SetCompanyID(company).SetEmail(legacy + "-collection@test").SetName("Reader").SetPasswordHash("hash").SetRole(legacy).SaveX(ctx)
		if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
			t.Fatal(err)
		}
		client.JobAssignment.Create().SetJobID(job.ID).SetUserID(u.ID).SaveX(ctx)
		users = append(users, u.ID)
	}
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetJobID(job.ID).SetClockIn(now).SetClockOut(now.Add(time.Hour)).SetNotes("Visible navigation time").SaveX(ctx)
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(now.Add(-time.Hour)).SetClockOut(now).SetNotes("HIDDEN navigation time").SaveX(ctx)
	for _, record := range []struct {
		typ string
		id  int64
	}{{"job", job.ID}, {"estimate", estimate.ID}, {"invoice", invoice.ID}, {"project", project.ID}} {
		if _, err := pool.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id,metadata) VALUES($1,$2,'updated',$3,$4,'{"entity_name":"PRIVATE SNAPSHOT","changes":{"financial":"PRIVATE FINANCIAL"}}')`, company, admin, record.typ, record.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, user := range users {
		cookie := sessionCookie(t, ctx, sessions, user)
		for _, dest := range []struct{ path, visible string }{
			{"/jobs", "Visible navigation Job"}, {"/estimates", "Visible navigation Estimate"}, {"/invoices", "Visible navigation Invoice"},
			{"/projects", "Visible navigation Project"}, {"/assets", "Visible navigation Asset"}, {"/time-entries", "Visible navigation time"},
			{"/schedule?tab=list&period=day&date=" + now.Format("2006-01-02"), "Visible navigation Job"},
			{"/activity", "Visible navigation"}, {"/search?q=navigation", "Visible navigation"},
		} {
			body := requestBody(t, router, cookie, "GET", dest.path, 200)
			assertContains(t, body, dest.visible)
			for _, hidden := range []string{"HIDDEN navigation", "FOREIGN", "UNDISCLOSED CUSTOMER", "PRIVATE FINANCIAL", "PRIVATE SNAPSHOT", `page=2`} {
				assertNotContains(t, body, hidden)
			}
		}
		// A visible list entry is a real current-authority destination.
		for _, path := range []string{fmt.Sprintf("/jobs/%d", job.ID), fmt.Sprintf("/estimates/%d", estimate.ID), fmt.Sprintf("/invoices/%d", invoice.ID), fmt.Sprintf("/projects/%d", project.ID)} {
			expectStatus(t, router, cookie, "GET", path, 200)
		}
	}
	// Assignment loss is applied before page counts/rows and activity cursors.
	client.Job.UpdateOneID(job.ID).SetDeletedAt(now).SaveX(ctx)
	for _, path := range []string{"/estimates", "/invoices", "/time-entries"} {
		body := requestBody(t, router, sessionCookie(t, ctx, sessions, users[0]), "GET", path, 200)
		assertNotContains(t, body, "Visible navigation")
	}
	client.Job.UpdateOneID(job.ID).ClearDeletedAt().SaveX(ctx)
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1`, users[0]); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, users[0])
	for _, path := range []string{"/jobs", "/estimates", "/invoices", "/time-entries", "/schedule?tab=list&period=day&date=" + now.Format("2006-01-02")} {
		body := requestBody(t, router, cookie, "GET", path, 200)
		assertNotContains(t, body, "Visible navigation")
		assertNotContains(t, body, "HIDDEN navigation")
	}
	for i := range grants {
		if grants[i].Permission == "time.view" {
			grants[i].Scope = authorization.Own
		} else if grants[i].Scope == authorization.Assigned {
			grants[i].Scope = authorization.All
		}
	}
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "All records and own time", grants, 1); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/jobs", "/estimates", "/invoices"} {
		body := requestBody(t, router, cookie, "GET", path, 200)
		assertContains(t, body, "HIDDEN navigation")
		assertContains(t, body, "page=2")
		assertNotContains(t, body, "FOREIGN")
		body = requestBody(t, router, cookie, "GET", path+"?search=Visible", 200)
		assertContains(t, body, "Visible navigation")
		assertNotContains(t, body, "page=2")
	}
	client.TimeEntry.Create().SetCompanyID(company).SetUserID(users[0]).SetClockIn(now).SetClockOut(now.Add(time.Hour)).SetNotes("Own navigation time").SaveX(ctx)
	body := requestBody(t, router, cookie, "GET", "/time-entries", 200)
	assertContains(t, body, "Own navigation time")
	assertNotContains(t, body, "Visible navigation time")
	assertNotContains(t, body, "HIDDEN navigation time")
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "No navigation records", nil, 2); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/jobs", "/projects", "/assets", "/estimates", "/invoices", "/time-entries", "/schedule", "/activity", "/search?q=navigation"} {
		expectStatus(t, router, cookie, "GET", path, 403)
	}
}

func TestNavigationWorkflowOnlyHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "configuration.workflow.view", Scope: authorization.Configuration}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Only workflow", grants)
	if err != nil {
		t.Fatal(err)
	}
	var user int64
	if err = pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,'navigation@test','Navigation','x','tech',$2) RETURNING id`, company, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user)
	for _, path := range []string{"/preferences", "/settings/statuses/job", "/settings/statuses/project", "/settings/statuses/estimate", "/settings/statuses/invoice"} {
		body := requestBody(t, router, cookie, "GET", path, 200)
		sidebar := sidebarHTML(t, body)
		for _, typ := range []string{"job", "project", "estimate", "invoice"} {
			assertContains(t, sidebar, fmt.Sprintf(`href="/settings/statuses/%s"`, typ))
		}
		for _, denied := range []string{"/settings", "/settings/mail", "/settings/maps", "/settings/custom-fields", "/users", "/jobs", "/schedule", "/time-entries"} {
			assertNotContains(t, sidebar, `href="`+denied+`"`)
		}
		if strings.HasPrefix(path, "/settings/statuses/") {
			assertNotContains(t, body, `href="/settings"`)
			assertContains(t, body, `href="/">`)
		}
	}
	expectStatus(t, router, cookie, "GET", "/settings", 403)
}

func TestNavigationIndependentConfigurationHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	for _, section := range []struct{ permission, path string }{
		{"configuration.company.view", "/settings"},
		{"configuration.documents.view", "/settings"},
		{"configuration.mail.view", "/settings/mail"},
		{"configuration.maps.view", "/settings/maps"},
		{"configuration.assets.view", "/settings/assets"},
		{"configuration.tags.view", "/tags"},
		{"configuration.fields.view", "/settings/custom-fields"},
	} {
		t.Run(section.permission, func(t *testing.T) {
			role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, section.permission, []authorization.Grant{{Permission: section.permission, Scope: authorization.Configuration}})
			if err != nil {
				t.Fatal(err)
			}
			var user int64
			if err = pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,$2,'Config reader','x','tech',$3) RETURNING id`, company, section.permission+"@test", role).Scan(&user); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, user)
			body := requestBody(t, router, cookie, "GET", "/preferences", 200)
			assertContains(t, sidebarHTML(t, body), `href="`+section.path+`"`)
			body = requestBody(t, router, cookie, "GET", section.path, 200)
			for _, denied := range []string{"/settings", "/settings/mail", "/settings/maps", "/settings/open-tracking", "/settings/statuses/job", "/settings/custom-fields", "/users"} {
				if denied != section.path {
					assertNotContains(t, body, `href="`+denied+`"`)
				}
			}
			assertContains(t, body, `href="/"`)
		})
	}
}

func TestNavigationEffectiveGrantsAndRenameHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	grants := []authorization.Grant{
		{Permission: "customer.view", Scope: authorization.All},
		{Permission: "job.view", Scope: authorization.Assigned},
		{Permission: "project.view", Scope: authorization.Context},
		{Permission: "asset.view", Scope: authorization.Context},
		{Permission: "item.view", Scope: authorization.All},
		{Permission: "time.view", Scope: authorization.Own},
		{Permission: "configuration.workflow.view", Scope: authorization.Configuration},
	}
	var first string
	for i, legacy := range []string{"tech", "dispatcher"} {
		role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, fmt.Sprintf("Equal grants %d", i), grants)
		if err != nil {
			t.Fatal(err)
		}
		var user int64
		if err = pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,$2,'Equal','x',$3,$4) RETURNING id`, company, fmt.Sprintf("equal%d@test", i), legacy, role).Scan(&user); err != nil {
			t.Fatal(err)
		}
		cookie := sessionCookie(t, ctx, sessions, user)
		sidebar := sidebarHTML(t, requestBody(t, router, cookie, "GET", "/preferences", 200))
		for _, visible := range []string{"/customers", "/jobs", "/schedule", "/items", "/time-entries", "/settings/statuses/job"} {
			assertContains(t, sidebar, `href="`+visible+`"`)
		}
		for _, hidden := range []string{"/projects", "/assets", "/estimates", "/invoices", "/directory", "/users", "/settings"} {
			assertNotContains(t, sidebar, `href="`+hidden+`"`)
		}
		if i == 0 {
			first = sidebar
		} else if sidebar != first {
			t.Fatal("equal grants produced different navigation")
		}
		if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, fmt.Sprintf("Renamed %d", i), grants, 1); err != nil {
			t.Fatal(err)
		}
		if got := sidebarHTML(t, requestBody(t, router, cookie, "GET", "/preferences", 200)); got != sidebar {
			t.Fatal("Role rename changed navigation")
		}
		if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, fmt.Sprintf("Empty %d", i), nil, 2); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/preferences", "/"} {
			body := requestBody(t, router, cookie, "GET", path, 200)
			for _, hidden := range []string{"/customers", "/jobs", "/schedule", "/items", "/time-entries", "/settings/statuses/job", "/projects", "/assets", "/estimates", "/invoices", "/activity", "/directory", "/users", "/settings"} {
				assertNotContains(t, body, `href="`+hidden+`"`)
			}
			assertNotContains(t, sidebarHTML(t, body), `action="/search"`)
		}
	}
}

func TestNavigationCurrentAuthorityCreationHTTP(t *testing.T) {
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	var grants []authorization.Grant
	for _, domain := range []string{"customer", "job", "project", "asset", "estimate", "invoice", "item"} {
		grants = append(grants, authorization.Grant{Permission: domain + ".view", Scope: authorization.All}, authorization.Grant{Permission: domain + ".create", Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Creator", grants)
	if err != nil {
		t.Fatal(err)
	}
	var user int64
	if err = pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,'creator-nav@test','Creator','x','tech',$2) RETURNING id`, company, role).Scan(&user); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, user)
	sidebar := sidebarHTML(t, requestBody(t, router, cookie, "GET", "/preferences", 200))
	// Both collection reads and independent creation use current authority.
	for _, path := range []string{"/projects", "/assets", "/estimates", "/invoices", "/activity"} {
		assertContains(t, sidebar, `href="`+path+`"`)
		expectStatus(t, router, cookie, "GET", path, 200)
	}
	for _, path := range []string{"/customers/new", "/jobs/new", "/projects/new", "/assets/new", "/estimates/new", "/invoices/new", "/items/new"} {
		assertContains(t, sidebar, `href="`+path+`"`)
		expectStatus(t, router, cookie, "GET", path, 200)
	}
}

func TestNavigationDashboardSectionsUseCurrentGrantsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Job overview", []authorization.Grant{{Permission: "job.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private dashboard Customer").SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("One counted Job").SaveX(ctx)
	for _, legacy := range []string{"tech", "dispatcher"} {
		u := client.User.Create().SetCompanyID(company).SetEmail(legacy + "-overview@test").SetName("Overview").SetPasswordHash("hash").SetRole(legacy).SaveX(ctx)
		if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
			t.Fatal(err)
		}
		body := requestBody(t, router, sessionCookie(t, ctx, sessions, u.ID), "GET", "/", 200)
		assertContains(t, body, `class="dashboard-metric">1</p>`)
		assertContains(t, body, `>View Jobs</a>`)
		assertNotContains(t, body, "Private dashboard Customer")
		assertNotContains(t, body, `>View Invoices</a>`)
		assertNotContains(t, body, `>View Customers</a>`)
	}
}
