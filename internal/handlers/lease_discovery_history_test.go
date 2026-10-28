package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseRegistrationDiscoveryAndFacetHistoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Registration choice").SaveX(ctx)
	assertContains(t, requestBody(t, web, cookie, "GET", "/leases/new", 200), "Registration choice")
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "title": {"Private base discovery"}}, 303)
	assertContains(t, requestBody(t, web, cookie, "GET", "/leases?search=Private", 200), "Private base discovery")
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/history", 200), "Private base discovery")
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	if strings.Contains(requestBody(t, web, cookie, "GET", "/leases", 200), "Private base discovery") {
		t.Fatal("archived Draft in ordinary discovery")
	}
	assertContains(t, requestBody(t, web, cookie, "GET", "/leases?state=archived", 200), "Private base discovery")
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Financial history only", []authorization.Grant{{Permission: "lease.billing.view", Scope: authorization.All}, {Permission: "lease.archived.view", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Financial reader").SetEmail("financial-history65@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	financial := sessionCookie(t, ctx, sessions, u.ID)
	for _, route := range []string{"/leases?state=archived", path + "/billing", path + "/billing/history"} {
		body := requestBody(t, web, financial, "GET", route, 200)
		if strings.Contains(body, "Private base discovery") || strings.Contains(body, path+`"`) || strings.Contains(body, "Register Draft") {
			t.Fatalf("financial-only response leaked base content/navigation: %s", route)
		}
	}
	requestBody(t, web, financial, "GET", path+"/history", 404)
	requestBody(t, web, financial, "GET", "/leases/new", 403)
}
