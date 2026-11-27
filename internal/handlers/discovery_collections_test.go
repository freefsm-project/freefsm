package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"golang.org/x/net/html"
)

func discoveryCollectionLinks(t *testing.T, body string, known map[string]bool) []string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	historyCheck(t, err)
	out := []string{}
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, attr := range n.Attr {
				if attr.Key == "href" && known[attr.Val] && !seen[attr.Val] {
					seen[attr.Val] = true
					out = append(out, attr.Val)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return out
}

func TestDiscoveryTenCollectionsPageSizesHTTP(t *testing.T) {
	for _, kind := range []string{"customer", "contact", "location", "asset", "job", "project", "estimate", "invoice", "lease", "item"} {
		t.Run(kind, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			ctx := context.Background()
			c := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Pagination parent").SaveX(ctx)
			typ := f.client.AssetType.Create().SetCompanyID(f.company).SetName("Pagination equipment").SaveX(ctx)
			known := map[string]bool{}
			want := []string{}
			for i := 0; i < 101; i++ {
				title := fmt.Sprintf("Collection106 %03d", i)
				var id int64
				switch kind {
				case "customer":
					id = f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName(title).SaveX(ctx).ID
				case "contact":
					id = f.client.CustomerContact.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetFirstName(title).SaveX(ctx).ID
				case "location":
					id = f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(c.ID).SetTitle(title).SaveX(ctx).ID
				case "asset":
					id = f.client.Asset.Create().SetCompanyID(f.company).SetOwnershipClass("customer_owned").SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetName(title).SaveX(ctx).ID
				case "job":
					id = f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetJobType(title).SaveX(ctx).ID
				case "project":
					id = f.client.Project.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetName(title).SaveX(ctx).ID
				case "estimate":
					id = f.client.Estimate.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetTitle(title).SaveX(ctx).ID
				case "invoice":
					id = f.client.Invoice.Create().SetCompanyID(f.company).SetCustomerID(c.ID).SetTitle(title).SetInvoiceNumber(int64(i + 1)).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx).ID
				case "item":
					id = f.client.Item.Create().SetCompanyID(f.company).SetName(title).SaveX(ctx).ID
				case "lease":
					svc := authorization.New(f.pool)
					actor := authorization.Actor{UserID: f.admin}
					l, err := svc.RegisterDraftLease(ctx, actor, c.ID)
					historyCheck(t, err)
					id = l.ID
					historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, id, authorization.DraftLeaseDetailsInput{Title: title}))
				}
				path := fmt.Sprintf("/%ss/%d", kind, id)
				known[path] = true
				want = append(want, path)
			}
			route := "/" + kind + "s"
			for _, size := range []int{25, 50, 100} {
				all := []string{}
				for page := 1; page <= (101+size-1)/size; page++ {
					query := fmt.Sprintf("%s?q=Collection106&per_page=%d&page=%d", route, size, page)
					body := requestBody(t, f.web, f.cookie, "GET", query, 200)
					got := discoveryCollectionLinks(t, body, known)
					discoveryEqual(t, len(got), min(size, 101-(page-1)*size))
					discoveryEqual(t, discoveryCollectionLinks(t, requestBody(t, f.web, f.cookie, "GET", query, 200), known), got)
					all = append(all, got...)
				}
				// Dedicated existing lists may keep their documented default sort;
				// every size must nevertheless traverse precisely the same set.
				seen := map[string]bool{}
				for _, path := range all {
					if seen[path] {
						t.Fatalf("duplicate across pages: %s", path)
					}
					seen[path] = true
				}
				discoveryEqual(t, len(seen), len(want))
			}
			discoveryEqual(t, len(discoveryCollectionLinks(t, requestBody(t, f.web, f.cookie, "GET", route+"?q=Collection106", 200), known)), 25)
			requestBody(t, f.web, f.cookie, "GET", route+"?per_page=26", 400)
			svc := authorization.New(f.pool)
			state, err := svc.LoadListState(ctx, authorization.Actor{UserID: f.admin}, kind+"s")
			historyCheck(t, err)
			discoveryEqual(t, state.Revision, int64(0))
			r := httptest.NewRequest("POST", route+"/state", strings.NewReader(url.Values{"query": {"q=Collection106&per_page=50"}, "revision": {"0"}}.Encode()))
			r.AddCookie(f.cookie)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.Header.Set("X-List-State", "save")
			w := httptest.NewRecorder()
			f.web.ServeHTTP(w, r)
			if w.Code != 204 {
				t.Fatalf("collection state save=%d: %s", w.Code, w.Body.String())
			}
			state, err = svc.LoadListState(ctx, authorization.Actor{UserID: f.admin}, kind+"s")
			historyCheck(t, err)
			discoveryEqual(t, state.Values.Get("per_page"), "50")
			fresh := sessionCookie(t, ctx, f.sessions, f.admin)
			discoveryEqual(t, len(discoveryCollectionLinks(t, requestBody(t, f.web, fresh, "GET", route, 200), known)), 50)
			discoveryEqual(t, len(discoveryCollectionLinks(t, requestBody(t, f.web, fresh, "GET", route+"?q=NoMatch106", 200), known)), 0)
			afterURL, err := svc.LoadListState(ctx, authorization.Actor{UserID: f.admin}, kind+"s")
			historyCheck(t, err)
			discoveryEqual(t, afterURL, state)
			for _, query := range []string{"page=0", "page=1&page=2", "sort=unsupported", "unexpected=1"} {
				requestBody(t, f.web, fresh, "GET", route+"?"+query, 400)
			}
			reset := httptest.NewRequest("POST", route+"/reset", strings.NewReader(url.Values{"revision": {fmt.Sprint(state.Revision)}}.Encode()))
			reset.AddCookie(fresh)
			reset.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			resetResponse := httptest.NewRecorder()
			f.web.ServeHTTP(resetResponse, reset)
			discoveryEqual(t, resetResponse.Code, 303)
			cleared, err := svc.LoadListState(ctx, authorization.Actor{UserID: f.admin}, kind+"s")
			historyCheck(t, err)
			discoveryEqual(t, len(cleared.Values), 0)
			discoveryEqual(t, len(discoveryCollectionLinks(t, requestBody(t, f.web, fresh, "GET", route+"?q=Collection106", 200), known)), 25)

			uid, role, reader := f.reader(t, "CollectionReader"+kind, authorization.Grant{Permission: kind + ".view", Scope: authorization.All})
			saveReader := func() *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", route+"/state", strings.NewReader(url.Values{"query": {"q=Collection106&per_page=50"}, "revision": {"0"}}.Encode()))
				r.AddCookie(reader)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.Header.Set("X-List-State", "save")
				w := httptest.NewRecorder()
				f.web.ServeHTTP(w, r)
				return w
			}
			discoveryEqual(t, saveReader().Code, 204)
			_, err = svc.SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "CollectionReader"+kind, nil, 1)
			historyCheck(t, err)
			for _, response := range []*httptest.ResponseRecorder{discoveryCall(f.web, reader, "GET", route), saveReader()} {
				if response.Code != 403 && response.Code != 404 {
					t.Fatalf("revoked collection returned %d: %s", response.Code, response.Body.String())
				}
				discoveryEqual(t, len(discoveryCollectionLinks(t, response.Body.String(), known)), 0)
			}
			retained, err := svc.LoadListState(ctx, authorization.Actor{UserID: uid}, kind+"s")
			historyCheck(t, err)
			discoveryEqual(t, retained.Revision, int64(1))
		})
	}
}
