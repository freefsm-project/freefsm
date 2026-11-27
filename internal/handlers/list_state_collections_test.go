package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

// Exercise installed routes rather than the parser alone: every dedicated
// collection must use the read boundary on both GET and preference POST.
func TestMainCollectionStateHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	authority := authorization.New(f.pool)
	actor := authorization.Actor{UserID: f.admin}
	for _, key := range []string{"customers", "contacts", "locations", "assets", "jobs", "projects", "estimates", "invoices", "leases", "items"} {
		t.Run(key, func(t *testing.T) {
			path := "/" + key
			get := discoveryCall(f.web, f.cookie, http.MethodGet, path+"?search=List106&per_page=50&page=2")
			if get.Code != http.StatusOK {
				t.Fatalf("GET %s: %d %s", path, get.Code, get.Body.String())
			}
			canonical, err := url.Parse(get.Header().Get("Content-Location"))
			historyCheck(t, err)
			if canonical.Path != path || canonical.Query().Get("per_page") != "50" {
				t.Fatalf("canonical state lost: %s", canonical)
			}
			before, err := authority.LoadListState(ctx, actor, key)
			historyCheck(t, err)
			if before.Revision != 0 {
				t.Fatal("GET mutated preferences")
			}
			save := collectionStatePOST(f.web, f.cookie, path+"/state", url.Values{"query": {canonical.RawQuery}, "revision": {"0"}})
			if save.Code != http.StatusNoContent {
				t.Fatalf("state POST %s: %d %s", path, save.Code, save.Body.String())
			}
			after, err := authority.LoadListState(ctx, actor, key)
			historyCheck(t, err)
			if after.Revision != 1 || after.Values.Get("per_page") != "50" || after.Values.Has("page") {
				t.Fatalf("wrong durable state: %#v", after)
			}
			restored := discoveryCall(f.web, f.cookie, http.MethodGet, path)
			if restored.Code != http.StatusOK {
				t.Fatalf("restore %s: %d %s", path, restored.Code, restored.Body.String())
			}
			restoredURL, err := url.Parse(restored.Header().Get("Content-Location"))
			historyCheck(t, err)
			if restoredURL.Query().Get("per_page") != "50" || restoredURL.Query().Get("page") != "1" {
				t.Fatalf("lost saved size or persisted page: %s", restoredURL)
			}
			override := discoveryCall(f.web, f.cookie, http.MethodGet, path+"?search=")
			if override.Code != http.StatusOK {
				t.Fatalf("override %s: %d %s", path, override.Code, override.Body.String())
			}
			overrideURL, err := url.Parse(override.Header().Get("Content-Location"))
			historyCheck(t, err)
			if overrideURL.Query().Get("per_page") != "25" {
				t.Fatalf("URL merged saved size: %s", overrideURL)
			}
			for _, query := range []string{"unknown=1", "per_page=26", "page=0", "page=1&page=2", "sort=not-a-sort"} {
				bad := discoveryCall(f.web, f.cookie, http.MethodGet, path+"?"+query)
				if bad.Code != http.StatusBadRequest {
					t.Fatalf("%s?%s: %d", path, query, bad.Code)
				}
			}
			reset := collectionStatePOST(f.web, f.cookie, path+"/reset", url.Values{"revision": {fmt.Sprint(after.Revision)}})
			if reset.Code != http.StatusSeeOther {
				t.Fatalf("reset %s: %d %s", path, reset.Code, reset.Body.String())
			}
			stale := collectionStatePOST(f.web, f.cookie, path+"/state", url.Values{"query": {canonical.RawQuery}, "revision": {fmt.Sprint(after.Revision)}})
			if stale.Code != http.StatusConflict {
				t.Fatalf("stale POST after reset: %d %s", stale.Code, stale.Body.String())
			}
			resetState, err := authority.LoadListState(ctx, actor, key)
			historyCheck(t, err)
			if len(resetState.Values) != 0 {
				t.Fatal("stale preference resurrected Reset")
			}
		})
	}
}

func TestMainCollectionPreferenceReauthorizesHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	_, _, cookie := f.reader(t, "ListCustomerOnly", authorization.Grant{Permission: "customer.view", Scope: authorization.All})
	for _, key := range []string{"contacts", "locations", "assets", "jobs", "projects", "estimates", "invoices", "leases", "items"} {
		t.Run(key, func(t *testing.T) {
			path := "/" + key
			denied := http.StatusForbidden
			// The shared validator preserves the child domain's concealment.
			if key == "contacts" || key == "locations" {
				denied = http.StatusNotFound
			}
			read := discoveryCall(f.web, cookie, http.MethodGet, path+"?search=")
			if read.Code != denied {
				t.Fatalf("GET %s = %d", path, read.Code)
			}
			save := collectionStatePOST(f.web, cookie, path+"/state", url.Values{"query": {"search=&per_page=25"}, "revision": {"0"}})
			if save.Code != denied {
				t.Fatalf("POST %s = %d %s", path, save.Code, save.Body.String())
			}
		})
	}
}

func collectionStatePOST(web http.Handler, cookie *http.Cookie, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-List-State", "save")
	w := httptest.NewRecorder()
	web.ServeHTTP(w, r)
	return w
}

func TestMainCollectionLiteralZeroIsTextHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	f.client.Item.Create().SetCompanyID(f.company).SetName("Existing visible item").SaveX(context.Background())
	for _, key := range []string{"customers", "contacts", "locations", "items", "assets", "projects", "jobs", "estimates", "invoices"} {
		for _, alias := range []string{"q", "search"} {
			t.Run(key+"/"+alias, func(t *testing.T) {
				body := requestBody(t, f.web, f.cookie, http.MethodGet, "/"+key+"?"+alias+"=0", http.StatusOK)
				assertContains(t, body, `data-list-empty="no-matches"`)
				assertContains(t, body, "Clear filters")
				assertNotContains(t, body, "Create your first")
			})
		}
	}
	// Numeric filter zero remains the explicit "all statuses" sentinel.
	body := requestBody(t, f.web, f.cookie, http.MethodGet, "/jobs?status_id=0&q=", http.StatusOK)
	assertContains(t, body, `data-list-empty="empty"`)
	assertNotContains(t, body, `data-list-empty="no-matches"`)
}
