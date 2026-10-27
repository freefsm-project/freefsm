package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestMasterItemRestoreIndependentAuthorityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	i := client.Item.Create().SetCompanyID(company).SetName("Retained catalog identity").SetSku("RESERVED-63").SaveX(ctx)
	path := fmt.Sprintf("/items/%d", i.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	itemPost(t, router, adminCookie, path+"/delete", nil, 303)
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "item.archived.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Restore without discovery", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Restorer").SetEmail("restore63@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	itemPost(t, router, cookie, path+"/restore", nil, 403)
	assertContains(t, requestBody(t, router, adminCookie, "GET", path, 200), "Archived on")
	grants = append(grants, authorization.Grant{Permission: "item.restore", Scope: authorization.All})
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Independent restorer", grants, 1); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, path+"/restore", nil, 303)
	assertNotContains(t, requestBody(t, router, cookie, "GET", path, 200), "Archived on")
}

func TestMasterItemArchivedListHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	i := client.Item.Create().SetCompanyID(company).SetName("Archived catalog history").SaveX(ctx)
	client.Item.Create().SetCompanyID(company).SetName("Current catalog choice").SaveX(ctx)
	path := fmt.Sprintf("/items/%d", i.ID)
	itemPost(t, router, cookie, path+"/delete", nil, 303)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/items", 200), "Archived catalog history")
	body := requestBody(t, router, cookie, "GET", "/items?archived=1", 200)
	assertContains(t, body, "Archived catalog history")
	assertNotContains(t, body, "Current catalog choice")
	assertNotContains(t, body, path+"/edit")
	assertNotContains(t, body, path+"/delete")
	assertContains(t, body, "Archived")
}

func TestMasterItemArchivedDiscoveryGatesHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	i := client.Item.Create().SetCompanyID(company).SetName("Archived base identity").SetSku("HIDDEN-ARCHIVED-SKU").SetUnitCost(963.17).SaveX(ctx)
	path := fmt.Sprintf("/items/%d", i.ID)
	itemPost(t, router, adminCookie, path+"/delete", nil, 303)
	svc := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "item.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Catalog discovery", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Reader").SetEmail("archive-reader63@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/items", 200), "Archived Items")
	expectStatus(t, router, cookie, "GET", "/items?archived=1", 403)
	grants = append(grants, authorization.Grant{Permission: "item.archived.view", Scope: authorization.All})
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Catalog history reader", grants, 1); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/items?archived=1", 200)
	assertContains(t, body, "Archived base identity")
	assertContains(t, body, "Archived Items")
	for _, hidden := range []string{"HIDDEN-ARCHIVED-SKU", "963.17", path + "/delete", path + "/edit"} {
		assertNotContains(t, body, hidden)
	}
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/items?archived=1&search=HIDDEN-ARCHIVED-SKU", 200), "Archived base identity")
	assertNotContains(t, requestBody(t, router, cookie, "GET", path, 200), `>Restore</button>`)
}

func TestMasterItemArchivedListPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	i := client.Item.Create().SetCompanyID(company).SetName("Browser archived catalog").SetSku("BROWSER-63").SaveX(ctx)
	path := fmt.Sprintf("/items/%d", i.ID)
	itemPost(t, router, cookie, path+"/delete", nil, 303)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/master-item-archive.cjs", server.URL, cookie.Value, fmt.Sprint(i.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	assertNotContains(t, requestBody(t, router, cookie, "GET", path, 200), "Archived on")
	assertContains(t, requestBody(t, router, cookie, "GET", "/items", 200), "Browser archived catalog")
}
