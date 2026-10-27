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

func TestMasterItemRestoreRechecksWaitingArchivedViewHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	i := client.Item.Create().SetCompanyID(company).SetName("Hidden restore target").SaveX(ctx)
	path := fmt.Sprintf("/items/%d", i.ID)
	itemPost(t, web, sessionCookie(t, ctx, sessions, admin), path+"/delete", nil, 303)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Waiting Item restorer", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "item.archived.view", Scope: authorization.All}, {Permission: "item.restore", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Restorer").SetEmail("view-race63@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	before := historyDurableRows(t, pool, []string{"items", "authorization_audit", "activity_logs"})
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(context.Background())
	var holder int
	historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
	f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- f.call(creditCommand{path: path + "/restore"}) }()
	commercialWaiters(t, ctx, pool, holder, 1)
	_, err = barrier.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='item.archived.view'`, role)
	historyCheck(t, err)
	historyCheck(t, barrier.Commit(ctx))
	if response := <-done; response.Code != 403 {
		t.Fatalf("Restore without current archived View=%d: %s", response.Code, response.Body.String())
	}
	historyEqual(t, "archived View loss rejects whole Restore", historyDurableRows(t, pool, []string{"items", "authorization_audit", "activity_logs"}), before)
}

func TestMasterItemRestorePreservesSKUAndCapturedWorkHTTP(t *testing.T) {
	_, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Captured owner").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Reserved catalog identity").SetSku("RESERVED-RESTORE-63").SetUnitPrice(99).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Captured work").SaveX(ctx)
	jobPath, itemPath := fmt.Sprintf("/jobs/%d", j.ID), fmt.Sprintf("/items/%d", i.ID)
	values := url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Accepted capture","unit_price":11.25,"quantity":2}]`, i.ID)}}
	itemPost(t, web, cookie, jobPath, values, 303)
	expectStatus(t, web, cookie, "POST", jobPath+"/create-estimate", 303)
	e := client.Estimate.Query().OnlyX(ctx)
	documentCapture := e.LineItems
	itemPost(t, web, cookie, itemPath+"/delete", nil, 303)
	duplicate := url.Values{"name": {"Forbidden SKU reuse"}, "type": {"service"}, "sku": {"RESERVED-RESTORE-63"}}
	itemPost(t, web, cookie, "/items", duplicate, 409)
	var lines []map[string]any
	historyCheck(t, json.Unmarshal([]byte(client.Job.GetX(ctx, j.ID).LineItems), &lines))
	// A free-text addition is not a new selection of the retained archived Item.
	lines = append(lines, map[string]any{"title": "Optional new free-text work", "unit_price": 7.5, "quantity": 1})
	raw, err := json.Marshal(lines)
	historyCheck(t, err)
	values.Set("line_items", string(raw))
	itemPost(t, web, cookie, jobPath, values, 303)
	jobCapture := client.Job.GetX(ctx, j.ID).LineItems
	lines = append(lines, map[string]any{"item_id": i.ID, "title": "Forbidden new archived selection", "unit_price": 11.25, "quantity": 1})
	raw, err = json.Marshal(lines)
	historyCheck(t, err)
	values.Set("line_items", string(raw))
	itemPost(t, web, cookie, jobPath, values, 409)
	itemPost(t, web, cookie, itemPath+"/restore", url.Values{"unit_price": {"999"}, "sku": {"REPLACE-IDENTITY"}}, 303)
	if got := client.Item.GetX(ctx, i.ID); got.Sku != "RESERVED-RESTORE-63" || got.UnitPrice != 99 {
		t.Fatalf("Restore changed Item facts: %+v", got)
	}
	if client.Job.GetX(ctx, j.ID).LineItems != jobCapture || client.Estimate.GetX(ctx, e.ID).LineItems != documentCapture {
		t.Fatal("archive/Restore or rejected new selection rewrote captures/provenance")
	}
	itemPost(t, web, cookie, "/items", duplicate, 409)
	assertContains(t, requestBody(t, web, cookie, "GET", jobPath, 200), "11.25")
	assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/estimates/%d", e.ID), 200), "11.25")
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
