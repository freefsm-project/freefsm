package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseReportFilterRecoveryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "asset.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, actor, 0, "Report preferences", grants)
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetName("Report preferences").SetEmail("report-preferences74@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
	login := sessionCookie(t, ctx, sessions, user.ID)
	request := func(path string, saved *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(login)
		if saved != nil {
			r.AddCookie(saved)
		}
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		return w
	}
	w := request("/leases/report?search=Retained&placement=unplaced", nil)
	if w.Code != 200 {
		t.Fatalf("report: %d", w.Code)
	}
	assertContains(t, w.Body.String(), "No Leases available under your current permissions")
	var saved *http.Cookie
	for _, cookie := range w.Result().Cookies() {
		if cookie.Path == "/leases/report" {
			saved = cookie
		}
	}
	if saved == nil {
		t.Fatal("report did not remember per-user filters")
	}
	w = request("/leases/report", saved)
	if w.Code != 303 {
		t.Fatalf("restore: %d", w.Code)
	}
	assertContains(t, w.Header().Get("Location"), "search=Retained")
	w = request("/leases/report?search=Explicit", saved)
	assertContains(t, w.Body.String(), `value="Explicit"`)
	assertNotContains(t, w.Body.String(), `value="Retained"`)
	w = request("/leases/report?reset=1", saved)
	assertNotContains(t, w.Body.String(), `value="Retained"`)
	_, err = svc.SaveRole(ctx, actor, role, "Report preferences", grants[:1], 1)
	historyCheck(t, err)
	w = request("/leases/report", saved)
	if w.Code != 303 || w.Header().Get("Location") != "/leases/report?reset=1" {
		t.Fatalf("grant-loss recovery: %d %s", w.Code, w.Body.String())
	}
	requestBody(t, web, login, "GET", "/leases/report?placement=unplaced", 403)
	for _, boosted := range []bool{false, true} {
		r := httptest.NewRequest("GET", "/leases/report?as_of=bad&search=Keep+this&asset=invalid", nil)
		r.AddCookie(login)
		if boosted {
			r.Header.Set("HX-Request", "true")
			r.Header.Set("HX-Boosted", "true")
		}
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		if w.Code != 422 {
			t.Fatalf("validation status: %d", w.Code)
		}
		assertContains(t, w.Body.String(), `data-error-summary`)
		assertContains(t, w.Body.String(), `href="#report-as_of"`)
		assertContains(t, w.Body.String(), `value="bad"`)
		assertContains(t, w.Body.String(), `value="Keep this"`)
		assertNotContains(t, w.Body.String(), `value="invalid"`)
		assertNotContains(t, w.Body.String(), "matching Leases")
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Empty-state Customer").SaveX(ctx)
	_, err = svc.RegisterDraftLease(ctx, actor, customer.ID)
	historyCheck(t, err)
	w = request("/leases/report?search=NoMatch", nil)
	assertContains(t, w.Body.String(), "No Leases match these filters")
	assertNotContains(t, w.Body.String(), "No Leases available")
}
