package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestCustomerCascadeBlockerPreviewUsesCurrentScopedViewHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Blocked owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("VISIBLE ASSIGNED WORK").SaveX(ctx)
	client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("SECRET OTHER WORK").SaveX(ctx)
	client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("SECRET PROJECT").SaveX(ctx)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Scoped cascade reviewer", []authorization.Grant{
		{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.archive", Scope: authorization.All},
		{Permission: "job.view", Scope: authorization.Assigned},
	})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("scoped-cascade@test").SetName("Reviewer").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	base := fmt.Sprintf("/customers/%d", c.ID)
	body := requestBody(t, web, cookie, "GET", base+"/archive", 200)
	for _, text := range []string{"VISIBLE ASSIGNED WORK", "job.view", "project.view", "Restricted dependency", "Unarchived Job"} {
		assertContains(t, body, text)
	}
	for _, text := range []string{"SECRET OTHER WORK", "SECRET PROJECT", "Confirm archive", "archive_snapshot"} {
		assertNotContains(t, body, text)
	}
	var preview struct {
		Snapshot string
		Blockers []struct{ Kind, ID, Name, RequiredPermission string }
	}
	masterJSON(t, web, cookie, base+"/archive", &preview)
	if preview.Snapshot != "" || len(preview.Blockers) != 3 {
		t.Fatalf("blocked preview should have no commit token: %+v", preview)
	}
	for _, b := range preview.Blockers {
		if b.Name != "VISIBLE ASSIGNED WORK" && (b.ID != "" || b.Name != "") {
			t.Fatalf("restricted identity disclosed: %+v", b)
		}
	}
	_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID)
	historyCheck(t, err)
	body = requestBody(t, web, cookie, "GET", base+"/archive", 200)
	assertNotContains(t, body, "VISIBLE ASSIGNED WORK")
	masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {"forged"}}, 409)
}

func TestCustomerCascadeMissingChildViewIsNondisclosingReviewHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Restricted child owner").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	masterPost(t, web, sessionCookie(t, ctx, sessions, admin), base+"/contacts", url.Values{"first_name": {"SECRET PERSON"}}, 303)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Customer only reviewer", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.archive", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("restricted-cascade@test").SetName("Reviewer").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, web, cookie, "GET", base+"/archive", 200)
	for _, text := range []string{"contact.view", "contact.archive", "Restricted dependency"} {
		assertContains(t, body, text)
	}
	if strings.Contains(body, "SECRET PERSON") || strings.Contains(body, "Confirm archive") {
		t.Fatal("restricted child or executable confirmation leaked")
	}
	masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {"forged"}}, 403)
}
