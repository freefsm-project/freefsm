package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestCommercialArchivedTargetsDoNotExpandSourceAuthorityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Visible source Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Visible source Job").SaveX(ctx)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	paths := map[string]string{}
	for _, kind := range []string{"estimate", "invoice"} {
		fieldPost(t, web, adminCookie, "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"ARCHIVED TARGET " + kind}, "notes": {"PRIVATE ARCHIVED CONTENT"}, "line_items": {"[]"}, "tax_rate": {"0"}, "invoice_date": {"2026-01-02"}}, 303)
		var id int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM `+kind+`s WHERE customer_id=$1`, c.ID).Scan(&id))
		path := fmt.Sprintf("/%ss/%d", kind, id)
		paths[kind] = path
		if kind == "invoice" {
			fieldPost(t, web, adminCookie, path+"/finalize", nil, 303)
		}
		fieldPost(t, web, adminCookie, path+"/delete", nil, 303)
	}
	svc := authorization.New(pool)
	var grants []authorization.Grant
	for _, kind := range []string{"customer", "job", "estimate", "invoice"} {
		grants = append(grants, authorization.Grant{Permission: kind + ".view", Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Source visibility only", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("source-viewer@test").SetName("Source viewer").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	for _, source := range []string{fmt.Sprintf("/customers/%d", c.ID), fmt.Sprintf("/jobs/%d", j.ID), "/search?q=ARCHIVED"} {
		body := requestBody(t, web, cookie, "GET", source, 200)
		assertNotContains(t, body, "PRIVATE ARCHIVED CONTENT")
		if source == "/search?q=ARCHIVED" {
			assertNotContains(t, body, "ARCHIVED TARGET")
		}
		for _, path := range paths {
			assertNotContains(t, body, `href="`+path)
			assertNotContains(t, body, `hx-get="`+path)
		}
	}
	for kind, path := range paths {
		expectStatus(t, web, cookie, "GET", path, 403)
		expectStatus(t, web, cookie, "GET", path+"/pdf/preview", 404)
		expectStatus(t, web, cookie, "GET", fmt.Sprintf("/customers/%d/work?type=%s&archived=1", c.ID, kind), 403)
		fieldPost(t, web, cookie, path+"/restore", nil, 404)
	}
}
