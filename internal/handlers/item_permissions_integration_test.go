package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/justinas/nosurf"
)

func TestItemFacetsThroughProductionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Catalog reader").SetEmail("catalog@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Base reader", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	i := client.Item.Create().SetCompanyID(company).SetName("Visible item").SetSku("SECRET-SKU").SetUnitPrice(123.45).SetUnitCost(987.65).SaveX(ctx)
	body := requestBody(t, router, cookie, http.MethodGet, fmt.Sprintf("/items/%d", i.ID), 200)
	assertContains(t, body, "Visible item")
	for _, secret := range []string{"SECRET-SKU", "123.45", "987.65", "Unit Cost", "Unit Price"} {
		assertNotContains(t, body, secret)
	}
	expectStatus(t, router, cookie, "GET", "/items", 200)
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/items/%d/edit", i.ID), 403)
	body = requestBody(t, router, cookie, "GET", "/items?search=SECRET-SKU", 200)
	assertNotContains(t, body, "Visible item")
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Renamed Pricebook reader", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All}}, 1); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/items/%d", i.ID), 200)
	assertContains(t, body, "SECRET-SKU")
	assertContains(t, body, "123.45")
	assertNotContains(t, body, "987.65")
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Independent Cost editor", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "cost.view", Scope: authorization.All}, {Permission: "cost.edit", Scope: authorization.All}, {Permission: "cost.export", Scope: authorization.All}}, 2); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/items/%d/edit", i.ID), 200)
	assertContains(t, body, "987.65")
	assertNotContains(t, body, "123.45")
	assertNotContains(t, body, "SECRET-SKU")
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d", i.ID), url.Values{"unit_cost": {"765.43"}}, 303)
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Invalid Cost export", []authorization.Grant{{Permission: "cost.export", Scope: authorization.All}}); err == nil {
		t.Fatal("Cost Export accepted without View prerequisites")
	}
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Customer Context only", []authorization.Grant{{Permission: "customer.view", Scope: authorization.Context}, {Permission: "job.view", Scope: authorization.Assigned}}, 3); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/items/%d", i.ID), 403)
	office := client.User.Create().SetCompanyID(company).SetRole("dispatcher").SetName("Activity reader").SetEmail("activity43@test").SetPasswordHash("x").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, office.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie = sessionCookie(t, ctx, sessions, office.ID)
	client.ActivityLog.Create().SetCompanyID(company).SetActorID(admin).SetObjectType("item").SetObjectID(i.ID).SetAction("created").SetMetadata(`{"entity_name":"HIDDEN_ITEM_ACTIVITY"}`).SaveX(ctx)
	body = requestBody(t, router, cookie, "GET", "/activity?type=item", 200)
	assertContains(t, body, "No activity")
}

func itemPost(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values, want int) string {
	t.Helper()
	values = currentJobFormValues(t, router, cookie, path, values)
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("POST %s = %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w.Body.String()
}

func TestItemMaintenanceIndependentFacetsAndAtomicAudit(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	u := client.User.Create().SetCompanyID(company).SetName("Base editor").SetEmail("editor@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Base editor", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "item.create", Scope: authorization.All}, {Permission: "item.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	itemPost(t, router, cookie, "/items", url.Values{"name": {"Repeated"}, "type": {"service"}}, 303)
	itemPost(t, router, cookie, "/items", url.Values{"name": {"Repeated"}, "type": {"product"}}, 303)
	itemPost(t, router, cookie, "/items", url.Values{"name": {"Denied"}, "type": {"service"}, "unit_cost": {"0"}}, 403)
	itemPost(t, router, cookie, "/items", url.Values{"name": {" "}, "type": {"service"}}, 409)
	itemPost(t, router, cookie, "/items", url.Values{"name": {"Invalid"}, "type": {"other"}}, 409)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	itemPost(t, router, adminCookie, "/items", url.Values{"name": {"Negative"}, "type": {"service"}, "unit_cost": {"-1"}}, 409)
	itemPost(t, router, adminCookie, "/items", url.Values{"name": {"Malformed"}, "type": {"service"}, "unit_price": {"nonsense"}}, 409)
	i := client.Item.Create().SetCompanyID(company).SetName("Preserved facets").SetUnitCost(987.65).SetUnitPrice(123.45).SaveX(ctx)
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d", i.ID), url.Values{"name": {"Updated"}, "type": {"product"}}, 303)
	got := client.Item.GetX(ctx, i.ID)
	if got.UnitCost != 987.65 || got.UnitPrice != 123.45 || got.Type != "product" {
		t.Fatalf("lost facets: %+v", got)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_item_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='item' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_item_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_item_audit()`); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d", i.ID), url.Values{"name": {"Must roll back"}}, 409)
	if got := client.Item.GetX(ctx, i.ID); got.Name != "Updated" {
		t.Fatalf("audit failure committed: %+v", got)
	}
}

func TestItemSelectionRejectsWholeEditAndRetainsCapturedPrice(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Selection customer").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Catalog work").SetUnitPrice(20).SetUnitCost(998.76).SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Captured quote"}, "tax_rate": {"0"}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Negotiated","quantity":1,"unit_price":17}]`, i.ID)}}
	itemPost(t, router, cookie, "/estimates", values, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM estimates WHERE title='Captured quote'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d", i.ID), url.Values{"type": {"product"}, "unit_price": {"50"}}, 303)
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d/delete", i.ID), url.Values{}, 303)
	values.Set("title", "Existing capture")
	itemPost(t, router, cookie, fmt.Sprintf("/estimates/%d", id), values, 303)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d", id), 200)
	assertContains(t, body, "17.00")
	assertNotContains(t, body, "998.76")
	values.Set("title", "Must not commit")
	values.Set("line_items", fmt.Sprintf(`[{"item_id":%d,"title":"Negotiated","quantity":1,"unit_price":17},{"item_id":%d,"title":"New selection","quantity":1,"unit_price":50}]`, i.ID, i.ID))
	itemPost(t, router, cookie, fmt.Sprintf("/estimates/%d", id), values, 409)
	if got := client.Estimate.GetX(ctx, id); got.Title != "Existing capture" {
		t.Fatalf("partial commercial edit: %+v", got)
	}
}

func TestItemFieldAPILookupSelectionAndMaintenanceDenial(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Field catalog").SetEmail("fieldcatalog@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Field Item reader", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	i := client.Item.Create().SetCompanyID(company).SetName("Field selection").SetSku("FIELD-43").SetUnitPrice(23.45).SetUnitCost(876.54).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, u.ID, "Item harness")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	request := func(method, path string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	body := request("GET", "/items?search=FIELD-43", 200)
	assertContains(t, body, `"unit_price":"23.45"`)
	assertNotContains(t, body, "876.54")
	assertNotContains(t, body, "unit_cost")
	body = request("GET", fmt.Sprintf("/items/%d/selection", i.ID), 200)
	assertContains(t, body, `"name":"Field selection"`)
	assertNotContains(t, body, "cost")
	request("POST", "/items", 405)
	request("PATCH", fmt.Sprintf("/items/%d", i.ID), 405)
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "API base reader", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}}, 1); err != nil {
		t.Fatal(err)
	}
	body = request("GET", "/items", 200)
	assertContains(t, body, "Field selection")
	assertNotContains(t, body, "unit_price")
	assertNotContains(t, body, "FIELD-43")
	request("GET", fmt.Sprintf("/items/%d/selection", i.ID), 403)
	request("GET", "/items/999999/selection", 404)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 4); err != nil {
		t.Fatal(err)
	}
	request("GET", "/items", 404)
	request("GET", fmt.Sprintf("/items/%d/selection", i.ID), 404)
}

func TestItemConcurrentSKUClaimsAndWaitingSelectionRevocation(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	results := make(chan int, 2)
	for n := 0; n < 2; n++ {
		go func() {
			r := httptest.NewRequest("POST", "/items", strings.NewReader(url.Values{"name": {"Concurrent"}, "type": {"service"}, "sku": {"CLAIM-43"}}.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			results <- w.Code
		}()
	}
	a, b := <-results, <-results
	if !(a == 303 && b == 409 || a == 409 && b == 303) {
		t.Fatalf("SKU race statuses %d %d", a, b)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Selection writer").SetEmail("race43@test").SetPasswordHash("x").SetRole("dispatcher").SaveX(ctx)
	svc := authorization.New(pool)
	grants := []authorization.Grant{}
	for _, p := range []string{"item.view", "pricebook.view", "customer.view", "estimate.view", "estimate.create", "pricing.view"} {
		grants = append(grants, authorization.Grant{Permission: p, Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Selection writer", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	userCookie := sessionCookie(t, ctx, sessions, u.ID)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race Customer").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Race Item").SaveX(ctx)
	for _, change := range []string{"archive", "revoke"} {
		t.Run(change, func(t *testing.T) {
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(ctx)
			if _, err := blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", "/estimates", strings.NewReader(url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Waiting selection"}, "tax_rate": {"0"}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Pending","unit_price":1,"quantity":1}]`, i.ID)}}.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(userCookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				var waiting bool
				if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='advisory' AND query LIKE '%190019%')`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("selection did not reach transaction lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if change == "archive" {
				_, err = blocker.Exec(ctx, `UPDATE items SET deleted_at=now() WHERE id=$1`, i.ID)
			} else {
				_, err = blocker.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='pricebook.view'`, role)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w := <-done
			want := 409
			if change == "revoke" {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("waiting selection %d: %s", w.Code, w.Body.String())
			}
			if n := client.Estimate.Query().CountX(ctx); n != 0 {
				t.Fatalf("rejected selection committed %d estimates", n)
			}
			if change == "archive" {
				itemPost(t, router, cookie, fmt.Sprintf("/items/%d/restore", i.ID), url.Values{}, 303)
			}
		})
	}
}

func TestItemOfflineReferenceHarnessThroughProductionAPI(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	if _, err := exec.LookPath("node"); err != nil {
		t.Fatal("Node required for Item reference conformance harness")
	}
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	i := client.Item.Create().SetCompanyID(company).SetName("Offline captured work").SetUnitPrice(23.45).SetUnitCost(876.54).SaveX(ctx)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Offline customer").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Reference harness")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	cmd := exec.Command("node", "../api/v1/testdata/item-reference-conformance.cjs")
	cmd.Env = append(os.Environ(), "ITEM_API_URL="+server.URL, "ITEM_API_TOKEN="+token, "ITEM_ID="+fmt.Sprint(i.ID))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("offline harness: %v %s", err, out)
	}
	var line map[string]any
	if err := json.Unmarshal(out, &line); err != nil {
		t.Fatalf("capture: %v %s", err, out)
	}
	assertNotContains(t, string(out), "cost")
	if line["unit_price"] != 23.45 {
		t.Fatalf("wrong capture: %s", out)
	}
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d", i.ID), url.Values{"unit_price": {"99"}, "type": {"product"}}, 303)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Offline capture"}, "tax_rate": {"0"}, "line_items": {"[" + string(out) + "]"}}
	itemPost(t, router, cookie, "/estimates", values, 303)
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d/delete", i.ID), url.Values{}, 303)
	values.Set("title", "Archived offline selection")
	itemPost(t, router, cookie, "/estimates", values, 409)
}

func TestItemPermissionsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser Item editor", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}, {Permission: "item.create", Scope: authorization.All}, {Permission: "item.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("Browser Item editor").SetEmail("item-browser@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	csrf := middleware.BrowserCSRF(middleware.CSRFToken(router))
	csrf.SetFailureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "CSRF validation failed: "+nosurf.Reason(r).Error(), 400)
	}))
	mux.Handle("/", csrf)
	server := httptest.NewServer(mux)
	defer server.Close()
	bctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(bctx, "node", "testdata/browser/item-permissions.cjs", server.URL, cookie.Value, adminCookie.Value).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM items WHERE name='Browser Item retained' AND type='product' AND unit_price=45.67 AND unit_cost=987.65 AND sku='BROWSER-43'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("persisted facet edit count=%d err=%v", count, err)
	}
}

func TestItemCapturedJobValuesSurviveArchiveWhenCopiedToEstimate(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Historical capture customer").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Historical Item").SetUnitPrice(99).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Historical work").SetLineItems(fmt.Sprintf(`[{"item_id":%d,"title":"Historic price","quantity":1,"unit_price":11.25}]`, i.ID)).SaveX(ctx)
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d/delete", i.ID), url.Values{}, 303)
	expectStatus(t, router, cookie, "POST", fmt.Sprintf("/jobs/%d/create-estimate", j.ID), 303)
	e := client.Estimate.Query().OnlyX(ctx)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/estimates/%d", e.ID), 200)
	assertContains(t, body, "11.25")
	itemPost(t, router, cookie, fmt.Sprintf("/jobs/%d/invoices", j.ID), url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Copied Job invoice"}, "invoice_date": {"2026-10-11"}, "due_date": {"2026-10-12"}, "tax_rate": {"0"}, "line_items": {j.LineItems}}, 303)
}

func TestItemInlineCreationRequiresFacetsWithoutRequestingCost(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	u := client.User.Create().SetCompanyID(company).SetName("Pricebook editor").SetEmail("inline43@test").SetPasswordHash("x").SetRole("dispatcher").SaveX(ctx)
	grants := []authorization.Grant{}
	for _, permission := range []string{"item.view", "item.create", "pricebook.view", "pricebook.create", "customer.view", "estimate.view", "estimate.create", "pricing.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Pricebook without Cost", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, router, cookie, "GET", "/estimates/new", 200)
	assertContains(t, body, "item-create-dialog")
	assertNotContains(t, body, "newItem.unit_cost")
	assertNotContains(t, body, "'unit_cost'")
	body = itemPost(t, router, cookie, "/items/inline", url.Values{"name": {"Inline Pricebook"}, "type": {"service"}, "unit_price": {"13.50"}}, 201)
	assertContains(t, body, `"unit_price":13.5`)
	assertNotContains(t, body, "cost")
}

func TestItemInvoiceSelectionWritersRejectWithoutPartialEdits(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Invoice input").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Invoice Item").SetUnitCost(321.98).SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Invoice captured"}, "invoice_date": {"2026-10-11"}, "due_date": {"2026-10-12"}, "tax_rate": {"0"}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Captured","quantity":1,"unit_price":15}]`, i.ID)}}
	itemPost(t, router, cookie, "/invoices", values, 303)
	inv := client.Invoice.Query().OnlyX(ctx)
	values.Set("invoice_number", fmt.Sprint(inv.InvoiceNumber))
	itemPost(t, router, cookie, fmt.Sprintf("/items/%d/delete", i.ID), url.Values{}, 303)
	values.Set("title", "Invoice retained")
	itemPost(t, router, cookie, fmt.Sprintf("/invoices/%d", inv.ID), values, 303)
	values.Set("title", "Invalid new invoice")
	itemPost(t, router, cookie, "/invoices", values, 409)
	values.Set("line_items", fmt.Sprintf(`[{"item_id":%d,"title":"Captured","quantity":1,"unit_price":15},{"item_id":%d,"title":"Appended","quantity":1,"unit_price":15}]`, i.ID, i.ID))
	itemPost(t, router, cookie, fmt.Sprintf("/invoices/%d", inv.ID), values, 409)
	if got := client.Invoice.GetX(ctx, inv.ID); got.Title != "Invoice retained" {
		t.Fatalf("partial invoice: %+v", got)
	}
	if n := client.Invoice.Query().CountX(ctx); n != 1 {
		t.Fatalf("invalid create committed: %d", n)
	}
}

func TestItemJobSelectionWritersUseTransactionAndPreserveSnapshots(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin, Channel: "web"}
	acting := authorization.WithActor(ctx, svc, actor)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Job input").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Job Item").SaveX(ctx)
	jobs := services.NewJobService(client)
	lines := []services.LineItem{{ItemID: i.ID, Title: "Job capture", UnitPrice: 12.34, Quantity: 1.5}}
	j, err := jobs.Create(acting, company, services.JobCreateParams{CustomerID: c.ID, JobType: "Captured job", BillingType: "flat_rate", LineItems: lines, CustomFields: "[]"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetItemArchived(ctx, actor, i.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Update(admitCurrentJobRevision(t, acting, pool, j.ID), company, j.ID, services.JobUpdateParams{LineItems: &lines}); err != nil {
		t.Fatalf("existing capture rejected: %v", err)
	}
	if _, err := jobs.Create(acting, company, services.JobCreateParams{CustomerID: c.ID, JobType: "Invalid", LineItems: lines, CustomFields: "[]"}); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("new archived selection: %v", err)
	}
	lines = append(lines, lines[0])
	title := "Must not commit"
	if _, err := jobs.Update(admitCurrentJobRevision(t, acting, pool, j.ID), company, j.ID, services.JobUpdateParams{JobType: &title, LineItems: &lines}); !errors.Is(err, authorization.ErrConflict) {
		t.Fatalf("appended archived selection: %v", err)
	}
	got, err := jobs.GetByID(ctx, j.ID)
	if err != nil || got.JobType != "Captured job" {
		t.Fatalf("partial Job edit: %+v %v", got, err)
	}
	stored, err := services.DecodeLineItems(got.LineItems)
	if err != nil || len(stored) != 1 || stored[0].UnitPrice != 12.34 || stored[0].Quantity != 1.5 {
		t.Fatalf("changed capture: %+v %v", stored, err)
	}
}

func TestItemSelectionAuditFailureRollsBackCommercialCreation(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Audit Customer").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Audit Item").SaveX(ctx)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_selection_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='item' AND NEW.action='pricebook.view' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_selection_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_selection_audit()`); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Rejected audited capture"}, "tax_rate": {"0"}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Selected","quantity":1,"unit_price":15}]`, i.ID)}}
	t.Run("audit rejection status", func(t *testing.T) {
		itemPost(t, router, cookie, "/estimates", values, 409)
	})
	// A response-mapping failure must not skip the rollback checkpoint.
	if n := client.Estimate.Query().CountX(ctx); n != 0 {
		t.Fatalf("audit failure committed %d estimates", n)
	}
}

func TestItemSelectionRechecksSessionAtCommit(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Expiring session customer").SaveX(ctx)
	i := client.Item.Create().SetCompanyID(company).SetName("Waited Item").SaveX(ctx)
	if _, err := pool.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp()+interval '2 seconds' WHERE user_id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err := blocker.Exec(ctx, `SELECT id FROM items WHERE id=$1 FOR UPDATE`, i.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/invoices", strings.NewReader(url.Values{"customer_id": {fmt.Sprint(c.ID)}, "invoice_date": {"2026-10-11"}, "due_date": {"2026-10-12"}, "title": {"Expired before commit"}, "tax_rate": {"0"}, "line_items": {fmt.Sprintf(`[{"item_id":%d,"title":"Selected","quantity":1,"unit_price":1}]`, i.ID)}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w
	}()
	deadline := time.Now().Add(time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event='transactionid' AND query LIKE '%FROM items%FOR SHARE%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("selection did not reach Item row lock before expiry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(2100 * time.Millisecond)
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code != 403 {
		t.Fatalf("expired commit = %d: %s", w.Code, w.Body.String())
	}
	if n := client.Invoice.Query().CountX(ctx); n != 0 {
		t.Fatal("expired selection committed")
	}
}
