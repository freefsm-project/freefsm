package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestCustomerDashboardIndependentFailureHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Dashboard failure control").SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Unaffected operational work").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/customers/%d", c.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Unaffected operational work")
	if _, err := pool.Exec(ctx, `ALTER TABLE customer_contacts RENAME TO dashboard_unavailable_contacts`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := pool.Exec(ctx, `ALTER TABLE dashboard_unavailable_contacts RENAME TO customer_contacts`); err != nil {
			t.Error(err)
		}
	}()
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Unaffected operational work")
	assertContains(t, body, "Contacts unavailable")
	assertContains(t, body, "Retry")
	assertNotContains(t, body, "No contacts")
	assertContains(t, body, "Updated at")
	assertContains(t, body, "Refresh dashboard")
}

func TestCustomerDashboardWaitingCollectionRechecksAuthorityHTTP(t *testing.T) {
	for _, change := range []string{"grant", "assignment", "archive"} {
		t.Run(change, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			auth := authorization.New(pool)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Waiting dashboard reader", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.Assigned}})
			if err != nil {
				t.Fatal(err)
			}
			u := client.User.Create().SetCompanyID(company).SetEmail("waiting-dashboard@test").SetName("Waiting reader").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
			if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
				t.Fatal(err)
			}
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Waiting Customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Previously authorized work").SaveX(ctx)
			client.JobAssignment.Create().SetJobID(job.ID).SetUserID(u.ID).SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			path := fmt.Sprintf("/customers/%d/work?type=job", c.ID)
			assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Previously authorized work")
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			var blocker int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("GET", path, nil).WithContext(ctx)
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w
			}()
			waitExportBlocker(t, ctx, pool, blocker)
			switch change {
			case "grant":
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='job.view'`, role)
			case "assignment":
				_, err = tx.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, job.ID)
			case "archive":
				_, err = tx.Exec(ctx, `UPDATE jobs SET deleted_at=now() WHERE id=$1`, job.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-done:
				want := http.StatusOK
				if change == "grant" {
					want = http.StatusNotFound
				}
				if w.Code != want {
					t.Fatalf("status %d want %d: %s", w.Code, want, w.Body.String())
				}
				assertNotContains(t, w.Body.String(), "Previously authorized work")
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestCustomerDashboardOmitsUnauthorizedSectionsAndActionsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Customer operational reader", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Reader").SetEmail("dashboard-actions@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Visible master").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/customers/%d", c.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	for _, text := range []string{"Create Job", "Create Estimate", "Create Invoice", `aria-label="Related Work"`, "View all Related Work", "Current Assets", "Manage Contacts", "Manage Locations", "Receivables"} {
		assertNotContains(t, body, text)
	}
	requestBody(t, router, cookie, "GET", path+"/work", 404)
}

func TestCustomerDashboardDirectWorkAndCollectionsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Direct Customer").SaveX(ctx)
	project := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Direct Project").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(project.ID).SetJobType("Direct Job").SaveX(ctx)
	client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(job.ID).SetTitle("Direct Estimate").SaveX(ctx)
	client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("HIDDEN_CONVERTED").SetConversionHiddenAt(time.Now()).SaveX(ctx)
	client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(job.ID).SetTitle("Direct Invoice").SetInvoiceNumber(104).SetInvoiceDate(time.Now()).SetDueDate(time.Time{}).SaveX(ctx)
	archived := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("ARCHIVED_DIRECT_JOB").SetDeletedAt(time.Now()).SaveX(ctx)
	path := fmt.Sprintf("/customers/%d", c.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	for _, kind := range []string{"Project", "Job", "Estimate", "Invoice"} {
		assertContains(t, body, kind+"s (1)")
		if strings.Count(body, "Direct "+kind) != 1 {
			t.Fatalf("duplicate direct %s", kind)
		}
		collection := requestBody(t, router, cookie, "GET", path+"/work?type="+strings.ToLower(kind), 200)
		assertContains(t, collection, "Direct "+kind)
		assertNotContains(t, collection, "HIDDEN_CONVERTED")
	}
	assertNotContains(t, body, "ARCHIVED_DIRECT_JOB")
	body = requestBody(t, router, cookie, "GET", path+"/work?type=job&archived=1", 200)
	assertContains(t, body, "ARCHIVED_DIRECT_JOB")
	assertContains(t, body, "Archived")
	assertNotContains(t, body, fmt.Sprintf(`href="/jobs/%d"`, archived.ID))
	// Independent permissions: an unauthorized collection is not an empty one.
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Dashboard Customer only", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Limited").SetEmail("dashboard104@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	limited := sessionCookie(t, ctx, sessions, u.ID)
	body = requestBody(t, router, limited, "GET", path, 200)
	assertContains(t, body, "Jobs (0)")
	assertContains(t, body, "No related work")
	assertNotContains(t, body, "Projects (0)")
	requestBody(t, router, limited, "GET", path+"/work?type=project", 404)
	requestBody(t, router, cookie, "GET", path+"/work?type=lease", 400)
	for i := 0; i < 30; i++ {
		client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType(fmt.Sprintf("Recent batch %02d", i)).SaveX(ctx)
	}
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Jobs (31)")
	if strings.Count(body, "Recent batch") != 10 {
		t.Fatal("recent card does not show exactly ten authorized records")
	}
	assertNotContains(t, body, "Direct Job")
	body = requestBody(t, router, cookie, "GET", path+"/work?type=job&page=2", 200)
	assertContains(t, body, "Related Work (31)")
	assertContains(t, body, "Direct Job")
	assertNotContains(t, body, "Direct Estimate")
}

func TestCustomerDashboardSectionFailuresHTTP(t *testing.T) {
	for _, tc := range []struct{ table, section string }{
		{"payment_invoice_allocations", "Receivables"},
		{"customer_credits", "Customer Credit"},
		{"projects", "Related Work"},
		{"files", "Files"},
		{"custom_field_definitions", "Custom Fields"},
		{"tag_links", "Tags"},
	} {
		t.Run(tc.section, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent sections").SetServiceNotes("Retained service instructions").SaveX(ctx)
			client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Visible direct work").SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, admin)
			path := fmt.Sprintf("/customers/%d", c.ID)
			if _, err := pool.Exec(ctx, `ALTER TABLE `+tc.table+` RENAME TO dashboard_unavailable`); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := pool.Exec(ctx, `ALTER TABLE dashboard_unavailable RENAME TO `+tc.table); err != nil {
					t.Error(err)
				}
			}()
			body := requestBody(t, router, cookie, "GET", path, 200)
			assertContains(t, body, tc.section+" unavailable")
			assertContains(t, body, "Retained service instructions")
			if tc.section != "Related Work" {
				assertContains(t, body, "Visible direct work")
			} else {
				assertNotContains(t, body, "Jobs (0)")
			}
			assertNotContains(t, body, "SQLSTATE")
		})
	}
}

func TestCustomerDashboardAssetPeriodsHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Period Customer").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("NEXT_CUSTOMER_PRIVATE").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("company_owned").SetNumber("PLACED-104").SetName("Placed machine").SetAssetTypeID(typ.ID).SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetNumber("PAST-104").SetName("Former machine").SetAssetTypeID(typ.ID).SaveX(ctx)
	archived := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetNumber("ARCHIVED-ASSET-104").SetName("Archived machine").SetAssetTypeID(typ.ID).SetDeletedAt(time.Now()).SaveX(ctx)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/associations/transfer", a.ID), url.Values{"customer_id": {fmt.Sprint(next.ID)}, "location_id": {"0"}, "effective_date": {"2020-01-02"}, "reason": {"Transferred"}, "revision": {"0"}}, 303)
	path := fmt.Sprintf("/customers/%d", c.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "PLACED-104")
	assertContains(t, body, "Company-owned placement")
	assertContains(t, body, "Past Asset associations")
	assertContains(t, body, "PAST-104")
	assertContains(t, body, "Customer-owned ownership")
	assertNotContains(t, body, "NEXT_CUSTOMER_PRIVATE")
	assertNotContains(t, body, "ARCHIVED-ASSET-104")
	assertNotContains(t, body, fmt.Sprintf(`href="/assets/%d"`, a.ID))
	// Revisiting is a distinct retained period, not one invented lifetime span.
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/associations/transfer", a.ID), url.Values{"customer_id": {fmt.Sprint(c.ID)}, "location_id": {"0"}, "effective_date": {"2020-02-02"}, "reason": {"Returned owner"}, "revision": {"1"}}, 303)
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "2020-01-02")
	assertContains(t, body, "2020-02-02")
	assertContains(t, body, "Unknown — observed association")
	body = requestBody(t, router, cookie, "GET", path+"/asset-history?archived=1", 200)
	assertContains(t, body, "ARCHIVED-ASSET-104")
	assertContains(t, body, "Archived — historical reference")
	assertNotContains(t, body, fmt.Sprintf(`href="/assets/%d"`, archived.ID))
}

func TestCustomerDashboardArchivedMastersHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Archive dashboard").SaveX(ctx)
	contact := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("ARCHIVED_CONTACT").SaveX(ctx)
	location := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("ARCHIVED_LOCATION").SaveX(ctx)
	if _, err := pool.Exec(ctx, `UPDATE customer_contacts SET deleted_at=now() WHERE id=$1`, contact.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE locations SET deleted_at=now() WHERE id=$1`, location.ID); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/customers/%d", c.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertNotContains(t, body, "ARCHIVED_CONTACT")
	assertNotContains(t, body, "ARCHIVED_LOCATION")
	assertContains(t, body, "Archived Contacts")
	assertContains(t, body, "Archived Locations")
	// Explicit archive collections preserve references; the ordinary dashboard
	// must not silently revive either master after the Customer is restored.
	body = requestBody(t, router, cookie, "GET", path+"/contacts?archived=1", 200)
	assertContains(t, body, "ARCHIVED_CONTACT")
	body = requestBody(t, router, cookie, "GET", path+"/locations?archived=1", 200)
	assertContains(t, body, "ARCHIVED_LOCATION")
}
