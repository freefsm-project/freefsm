package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestWorkArchiveDiscoveryCurrentGrantsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Visible owner").SaveX(ctx)
	var jobStatus, projectStatus int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&jobStatus))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='project:completed' AND is_category_default`, company).Scan(&projectStatus))
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(projectStatus).SetName("CLOSEDPROJECTIDENTITY").SetDescription("PRIVATE PROJECT CONTENT").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(jobStatus).SetJobType("CLOSEDJOBIDENTITY").SetNotes("PRIVATE JOB CONTENT").SaveX(ctx)
	live := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetJobType("Visible continuing Job").SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, adminCookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Visible document"}, "line_items": {"[]"}, "tax_rate": {"0"}, "invoice_date": {"2026-01-02"}}, 303)
	var invoice int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE job_id=$1`, j.ID).Scan(&invoice))
	invoicePath := fmt.Sprintf("/invoices/%d", invoice)
	fieldPost(t, web, adminCookie, invoicePath+"/finalize", nil, 303)
	paths := map[string]string{"job": fmt.Sprintf("/jobs/%d", j.ID), "project": fmt.Sprintf("/projects/%d", p.ID)}
	for _, path := range paths {
		fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
	}
	auth := authorization.New(pool)
	grants := []authorization.Grant{}
	for _, kind := range []string{"customer", "job", "project", "invoice"} {
		grants = append(grants, authorization.Grant{Permission: kind + ".view", Scope: authorization.All})
	}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Work source viewer", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Source viewer").SetEmail("work-source@test").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	checkHidden := func() {
		t.Helper()
		for _, source := range []string{"/search?q=CLOSED", "/jobs?search=CLOSED", "/projects?search=CLOSED", fmt.Sprintf("/customers/%d", c.ID), fmt.Sprintf("/jobs/%d", live.ID), invoicePath} {
			body := requestBody(t, web, cookie, "GET", source, 200)
			assertNotContains(t, body, "PRIVATE JOB CONTENT")
			assertNotContains(t, body, "PRIVATE PROJECT CONTENT")
			for _, path := range paths {
				assertNotContains(t, body, `href="`+path+`"`)
				assertNotContains(t, body, `hx-get="`+path)
			}
			if source == "/search?q=CLOSED" || source == "/jobs?search=CLOSED" || source == "/projects?search=CLOSED" {
				assertNotContains(t, body, "CLOSEDJOBIDENTITY")
				assertNotContains(t, body, "CLOSEDPROJECTIDENTITY")
			}
		}
		for kind := range paths {
			expectStatus(t, web, cookie, "GET", fmt.Sprintf("/customers/%d/work?type=%s&archived=1", c.ID, kind), 403)
		}
	}
	checkHidden()
	for _, kind := range []string{"job", "project"} {
		grants = append(grants, authorization.Grant{Permission: kind + ".archived.view", Scope: authorization.All}, authorization.Grant{Permission: kind + ".restore", Scope: authorization.All})
	}
	_, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Work source viewer", grants, 1)
	historyCheck(t, err)
	for kind, path := range paths {
		body := requestBody(t, web, cookie, "GET", path, 200)
		assertContains(t, body, "Archived on")
		assertContains(t, body, path+"/restore")
		collection := requestBody(t, web, cookie, "GET", fmt.Sprintf("/customers/%d/work?type=%s&archived=1", c.ID, kind), 200)
		identity := "CLOSEDJOBIDENTITY"
		if kind == "project" {
			identity = "CLOSEDPROJECTIDENTITY"
		}
		assertContains(t, collection, identity)
		assertContains(t, collection, "Archived — historical reference")
	}
	// Revocation affects the next read, including controls, collection totals,
	// source-reference navigation and search. The legacy role name grants nothing.
	_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission IN ('job.restore','project.restore','job.archived.view','project.archived.view')`, role)
	historyCheck(t, err)
	checkHidden()
	for _, path := range paths {
		fieldPost(t, web, cookie, path+"/restore", nil, 404)
	}
}
