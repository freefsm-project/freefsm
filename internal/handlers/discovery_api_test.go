package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestDiscoveryFieldAPIFacetAndOfficeBoundary(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	uid, role, _ := f.reader(t, "APIDiscovery", authorization.Grant{Permission: "item.view", Scope: authorization.All}, authorization.Grant{Permission: "pricebook.view", Scope: authorization.All}, authorization.Grant{Permission: "cost.view", Scope: authorization.All}, authorization.Grant{Permission: "lease.view", Scope: authorization.All}, authorization.Grant{Permission: "lease.billing.view", Scope: authorization.All})
	item := f.client.Item.Create().SetCompanyID(f.company).SetName("API106 item").SetSku("API106-SECRET").SetUnitPrice(123.45).SetUnitCost(987.65).SaveX(ctx)
	token, err := f.sessions.CreateMobile(ctx, uid, "Discovery acceptance")
	historyCheck(t, err)
	api := apiv1.NewRouter(f.pool, f.client, f.sessions)
	call := func(path string, want int) string {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s = %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	body := call("/items?search=API106-SECRET", 200)
	assertContains(t, body, "API106 item")
	assertNotContains(t, body, "987.65")
	assertNotContains(t, body, "unit_cost")
	if strings.Count(body, `"name":"API106 item"`) != 1 {
		t.Fatalf("Item not single identity: %s", body)
	}
	body = call(fmt.Sprintf("/items/%d/selection", item.ID), 200)
	assertNotContains(t, body, "unit_cost")
	for _, path := range []string{"/search?q=API106", "/leases", "/leases/1", "/search/reset"} {
		call(path, 404)
	}
	call("/items?not_a_filter=1", 400)
	_, err = authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "API base", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}}, 1)
	historyCheck(t, err)
	body = call("/items?search=API106-SECRET", 200)
	assertNotContains(t, body, "API106 item")
	call(fmt.Sprintf("/items/%d/selection", item.ID), 403)
	call("/items/99999999/selection", 404)
	_, err = authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "API none", nil, 2)
	historyCheck(t, err)
	call("/items", 404)
}
