package handlers

import (
	"context"
	"encoding/base64"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/liststate"
	"github.com/jackc/pgx/v5"
)

func TestCollectionListSpecsRetainDomainFilters(t *testing.T) {
	valid := map[string]string{
		"customers": "search=Contact&archived=1&sort=name_desc",
		"contacts":  "search=Email&archived=1&sort=name_desc",
		"locations": "search=Address&archived=1&sort=name",
		"assets":    "search=Number&customer_id=12&asset_type_id=3&asset_status_id=4&archived=1",
		"jobs":      "search=Job&status_id=2&customer_id=3&scope=J&sort=schedule_desc",
		"projects":  "search=Project&status_id=2",
		"estimates": "search=Estimate&status_id=2&customer_id=3&scope=A&valid=current&sort=number_asc",
		"invoices":  "search=Invoice&status_id=2&customer_id=3&scope=J&sort=title_asc",
		"leases":    "search=Lease&state=all&facet=billing&lifecycle=active&queue=monthly&customer=2&asset=3&premises=4&ending_from=2026-01-01&ending_through=2026-12-31&placement=unplaced&billing=failed",
		"items":     "search=SKU&archived=1",
	}
	for key, raw := range valid {
		t.Run(key, func(t *testing.T) {
			values, err := url.ParseQuery(raw)
			historyCheck(t, err)
			q, err := liststate.Parse(values, collectionListSpec(key))
			historyCheck(t, err)
			if q.Values.Get("page") != "1" || q.Values.Get("per_page") != "25" {
				t.Fatalf("invalid pagination defaults: %v", q.Values)
			}
			for field, values := range values {
				if q.Values.Get(field) != values[0] {
					t.Fatalf("filter discarded: %s", field)
				}
			}
			for _, raw := range []string{"q=x&q=y", "search=x&search=y", "page=0", "page=1000001", "per_page=26", "sort=random", "unknown=1"} {
				values, err := url.ParseQuery(raw)
				historyCheck(t, err)
				if _, err := liststate.Parse(values, collectionListSpec(key)); err == nil {
					t.Fatalf("accepted %s", raw)
				}
			}
		})
	}
}

func TestCollectionMainRoutesStrictHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	for _, key := range []string{"customers", "contacts", "locations", "assets", "jobs", "projects", "estimates", "invoices", "leases", "items"} {
		t.Run(key, func(t *testing.T) {
			for _, raw := range []string{"q=x&search=y", "search=x&search=y", "page=0", "per_page=26", "sort=random", "unknown=1"} {
				requestBody(t, f.web, f.cookie, "GET", "/"+key+"?"+raw, 400)
			}
			for _, size := range []string{"25", "50", "100"} {
				requestBody(t, f.web, f.cookie, "GET", "/"+key+"?q=&per_page="+size, 200)
			}
		})
	}
}

func TestCollectionLeaseLegacyCookieIgnoredHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	parent := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Lease parent").SaveX(ctx)
	lease, err := authorization.New(f.pool).RegisterDraftLease(ctx, authorization.Actor{UserID: f.admin}, parent.ID)
	historyCheck(t, err)
	request := httptest.NewRequest("GET", "/leases", nil)
	request.AddCookie(f.cookie)
	request.AddCookie(&http.Cookie{Name: fmt.Sprintf("lease_query_%d", f.admin), Value: base64.RawURLEncoding.EncodeToString([]byte("search=NoSuchLease&facet=billing"))})
	response := httptest.NewRecorder()
	f.web.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("legacy cookie changed route: %d %s", response.Code, response.Body.String())
	}
	path := fmt.Sprintf("/leases/%d", lease.ID)
	if got := discoveryCollectionLinks(t, response.Body.String(), map[string]bool{path: true}); len(got) != 1 {
		t.Fatalf("legacy cookie hid current default results: %v", got)
	}
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == fmt.Sprintf("lease_query_%d", f.admin) {
			t.Fatal("legacy remembered state still written")
		}
	}
}

func TestCollectionProjectPaginationCurrentAuthorityHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	parent := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Undisclosed parent").SaveX(ctx)
	var other int64
	historyCheck(t, f.pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other collection','other-collection') RETURNING id`).Scan(&other))
	foreign := f.client.Customer.Create().SetCompanyID(other).SetDisplayName("Foreign parent").SaveX(ctx)
	known := map[string]bool{}
	stamp := time.Now()
	for i := 0; i < 31; i++ {
		row := f.client.Project.Create().SetCompanyID(f.company).SetCustomerID(parent.ID).SetName(fmt.Sprintf("Paging106 %02d", i)).SetUpdatedAt(stamp).SaveX(ctx)
		known[fmt.Sprintf("/projects/%d", row.ID)] = true
		f.client.Project.Create().SetCompanyID(other).SetCustomerID(foreign.ID).SetName("Paging106 foreign").SetUpdatedAt(stamp.Add(time.Hour)).SaveX(ctx)
	}
	_, role, cookie := f.reader(t, "ProjectOnlyPaging", authorization.Grant{Permission: "project.view", Scope: authorization.All})
	var previous []string
	for _, size := range []int{25, 50, 100} {
		var all []string
		for page := 1; page <= (31+size-1)/size; page++ {
			path := fmt.Sprintf("/projects?q=Paging106&per_page=%d&page=%d", size, page)
			body := requestBody(t, f.web, cookie, "GET", path, 200)
			assertNotContains(t, body, "Paging106 foreign")
			assertNotContains(t, body, "Undisclosed parent")
			links := discoveryCollectionLinks(t, body, known)
			discoveryEqual(t, len(links), min(size, 31-(page-1)*size))
			all = append(all, links...)
		}
		if previous != nil {
			discoveryEqual(t, all, previous)
		}
		previous = all
	}
	body := requestBody(t, f.web, cookie, "GET", "/projects?q=Paging106&page=1000000", 200)
	discoveryEqual(t, len(discoveryCollectionLinks(t, body, known)), 0)
	_, err := authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "ProjectOnlyPaging", nil, 1)
	historyCheck(t, err)
	requestBody(t, f.web, cookie, "GET", "/projects?q=Paging106", 403)
}

func TestCollectionLeaseExplicitBaseRequiresCurrentBaseViewHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	svc := authorization.New(f.pool)
	uid, role, cookie := f.reader(t, "LeaseFacetBase",
		authorization.Grant{Permission: "lease.view", Scope: authorization.All},
		authorization.Grant{Permission: "lease.billing.view", Scope: authorization.All})
	// An explicitly selected base facet is valid while base authority exists.
	saved := collectionStatePOST(f.web, cookie, "/leases/state", url.Values{
		"query": {"q=&facet=base"}, "revision": {"0"},
	})
	discoveryEqual(t, saved.Code, http.StatusNoContent)
	_, err := svc.SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "LeaseFacetBase",
		[]authorization.Grant{{Permission: "lease.billing.view", Scope: authorization.All}}, 1)
	historyCheck(t, err)
	actor := authorization.Actor{UserID: uid}
	before, err := svc.LoadListState(ctx, actor, "leases")
	historyCheck(t, err)
	discoveryEqual(t, before.Values.Get("facet"), "base")

	// Neither a shared URL nor restoration may reinterpret explicit base as
	// Billing after a grant change.
	requestBody(t, f.web, cookie, "GET", "/leases?q=&facet=base", http.StatusForbidden)
	requestBody(t, f.web, cookie, "GET", "/leases", http.StatusForbidden)
	denied := collectionStatePOST(f.web, cookie, "/leases/state", url.Values{
		"query": {"q=&facet=base"}, "revision": {fmt.Sprint(before.Revision)},
	})
	discoveryEqual(t, denied.Code, http.StatusForbidden)
	atomic, err := svc.SaveAuthorizedListState(ctx, actor, "leases", before.Values, before.Revision, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.ValidateCollectionListQuery(ctx, tx, p, "leases", before.Values)
	})
	if !errors.Is(err, authorization.ErrForbidden) || atomic.Revision != 0 || atomic.Values != nil {
		t.Fatalf("commit-time explicit base validation: %+v %v", atomic, err)
	}
	after, err := svc.LoadListState(ctx, actor, "leases")
	historyCheck(t, err)
	discoveryEqual(t, after, before)

	// Omission remains the documented automatic Billing-only default; an
	// explicit Billing facet remains usable too, without clearing blocked state.
	requestBody(t, f.web, cookie, "GET", "/leases?q=", http.StatusOK)
	requestBody(t, f.web, cookie, "GET", "/leases?q=&facet=billing", http.StatusOK)
	defaultSave := collectionStatePOST(f.web, cookie, "/leases/state", url.Values{
		"query": {"q="}, "revision": {fmt.Sprint(before.Revision)},
	})
	discoveryEqual(t, defaultSave.Code, http.StatusNoContent)
	requestBody(t, f.web, cookie, "GET", "/leases", http.StatusOK)
}

func TestCollectionDocumentUnicodeQueryBoundaryHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	for _, key := range []string{"estimates", "invoices"} {
		t.Run(key, func(t *testing.T) {
			revision := int64(0)
			for _, count := range []int{334, 499, 500} {
				for _, field := range []string{"q", "search"} {
					query := url.Values{field: {strings.Repeat("界", count)}}.Encode()
					requestBody(t, f.web, f.cookie, "GET", "/"+key+"?"+query, http.StatusOK)
					saved := collectionStatePOST(f.web, f.cookie, "/"+key+"/state", url.Values{
						"query": {query}, "revision": {fmt.Sprint(revision)},
					})
					discoveryEqual(t, saved.Code, http.StatusNoContent)
					revision++
					requestBody(t, f.web, f.cookie, "GET", "/"+key, http.StatusOK)
				}
			}
			for _, text := range []string{strings.Repeat("界", 501), "\xff"} {
				query := url.Values{"search": {text}}.Encode()
				requestBody(t, f.web, f.cookie, "GET", "/"+key+"?"+query, http.StatusBadRequest)
				saved := collectionStatePOST(f.web, f.cookie, "/"+key+"/state", url.Values{
					"query": {query}, "revision": {fmt.Sprint(revision)},
				})
				discoveryEqual(t, saved.Code, http.StatusBadRequest)
			}
			state, err := authorization.New(f.pool).LoadListState(context.Background(), authorization.Actor{UserID: f.admin}, key)
			historyCheck(t, err)
			discoveryEqual(t, state.Revision, revision)
			discoveryEqual(t, state.Values.Get("search"), strings.Repeat("界", 500))
		})
	}
}

func TestCollectionDocumentExportRetainsByteLimitHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	customer := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Export limit parent").SaveX(ctx)
	title := strings.Repeat("a", 1000)
	f.client.Estimate.Create().SetCompanyID(f.company).SetCustomerID(customer.ID).SetTitle(title).SaveX(ctx)
	f.client.Invoice.Create().SetCompanyID(f.company).SetCustomerID(customer.ID).SetTitle(title).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	for _, key := range []string{"estimates", "invoices"} {
		t.Run(key, func(t *testing.T) {
			path := "/" + key + "/export"
			for _, count := range []int{501, 1000} {
				search := strings.Repeat("a", count)
				requestBody(t, f.web, f.cookie, "GET", path+"?"+url.Values{"search": {search}, "scope": {"A"}}.Encode(), http.StatusOK)
				receipt := masterPost(t, f.web, f.cookie, path, url.Values{
					"search": {search}, "scope": {"A"}, "operation_id": {newOperationKey()},
				}, http.StatusSeeOther)
				_, err := exports.Product(f.pool).ProcessOne(ctx)
				historyCheck(t, err)
				body := requestBody(t, f.web, f.cookie, "GET", receipt+"/download", http.StatusOK)
				rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
				historyCheck(t, err)
				discoveryEqual(t, len(rows), 2)
				assertContains(t, body, title)
				// This remains an export-only acceptance set.
				requestBody(t, f.web, f.cookie, "GET", "/"+key+"?"+url.Values{"search": {search}}.Encode(), http.StatusBadRequest)
			}
			for _, search := range []string{strings.Repeat("a", 1001), strings.Repeat("界", 334), "\xff"} {
				requestBody(t, f.web, f.cookie, "GET", path+"?"+url.Values{"search": {search}, "scope": {"A"}}.Encode(), http.StatusBadRequest)
				masterPost(t, f.web, f.cookie, path, url.Values{
					"search": {search}, "scope": {"A"}, "operation_id": {newOperationKey()},
				}, http.StatusBadRequest)
			}
		})
	}
}

func TestCollectionFlashTransportRestoresSavedStateHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	svc := authorization.New(f.pool)
	actor := authorization.Actor{UserID: f.admin}
	for _, key := range []string{"customers", "contacts", "locations", "assets", "jobs", "projects", "estimates", "invoices", "leases", "items"} {
		t.Run(key, func(t *testing.T) {
			before, err := svc.SaveListState(ctx, actor, key, url.Values{"search": {"Remembered106"}, "per_page": {"50"}}, 0)
			historyCheck(t, err)
			response := discoveryCall(f.web, f.cookie, "GET", "/"+key+"?"+url.Values{"flash": {"<script>Saved106</script>"}}.Encode())
			discoveryEqual(t, response.Code, http.StatusOK)
			canonical, err := url.Parse(response.Header().Get("Content-Location"))
			historyCheck(t, err)
			discoveryEqual(t, canonical.Query().Get("search"), "Remembered106")
			discoveryEqual(t, canonical.Query().Get("per_page"), "50")
			if canonical.Query().Has("flash") {
				t.Fatal("flash entered canonical query")
			}
			assertContains(t, response.Body.String(), "&lt;script&gt;Saved106&lt;/script&gt;")
			assertNotContains(t, response.Body.String(), "<script>Saved106</script>")
			after, err := svc.LoadListState(ctx, actor, key)
			historyCheck(t, err)
			discoveryEqual(t, after, before)
			for _, query := range []string{"flash=one&flash=two", "unexpected106=1", "flash=" + strings.Repeat("x", 501)} {
				invalid := discoveryCall(f.web, f.cookie, "GET", "/"+key+"?"+query)
				discoveryEqual(t, invalid.Code, http.StatusBadRequest)
				assertNotContains(t, invalid.Body.String(), strings.Repeat("x", 501))
			}
			// A valid explicit state override still wins, even alongside flash.
			override := discoveryCall(f.web, f.cookie, "GET", "/"+key+"?search=&flash=Saved")
			discoveryEqual(t, override.Code, http.StatusOK)
			overrideURL, err := url.Parse(override.Header().Get("Content-Location"))
			historyCheck(t, err)
			discoveryEqual(t, overrideURL.Query().Get("search"), "")
			discoveryEqual(t, overrideURL.Query().Get("per_page"), "25")
			alias := discoveryCall(f.web, f.cookie, "GET", "/"+key+"?q=Alias106&flash=Saved")
			discoveryEqual(t, alias.Code, http.StatusOK)
			aliasURL, err := url.Parse(alias.Header().Get("Content-Location"))
			historyCheck(t, err)
			discoveryEqual(t, aliasURL.Query().Get("q"), "Alias106")
			discoveryEqual(t, aliasURL.Query().Get("per_page"), "25")
			conflict := discoveryCall(f.web, f.cookie, "GET", "/"+key+"?q=Alias106&search=Different106&flash=Saved")
			discoveryEqual(t, conflict.Code, http.StatusBadRequest)
			saved := collectionStatePOST(f.web, f.cookie, "/"+key+"/state", url.Values{
				"query": {"search=New106&flash=Saved"}, "revision": {fmt.Sprint(before.Revision)},
			})
			discoveryEqual(t, saved.Code, http.StatusNoContent)
			after, err = svc.LoadListState(ctx, actor, key)
			historyCheck(t, err)
			discoveryEqual(t, after.Values.Get("search"), "New106")
			if after.Values.Has("flash") {
				t.Fatal("flash persisted")
			}
		})
	}
}
