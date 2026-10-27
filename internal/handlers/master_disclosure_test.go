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
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestMasterArchivedSourceDiscoveryAndRestoreEligibilityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Visible source Customer").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("CLOSED_LOCATION").SetAddress1("PRIVATE_LOCATION_ADDRESS").SaveX(ctx)
	contact := client.CustomerContact.Create().SetCompanyID(company).SetCustomerID(c.ID).SetFirstName("CLOSED_CONTACT").SetPhone("PRIVATE_CONTACT_PHONE").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(typ.ID).SetName("CLOSED_ASSET").SetNotes("PRIVATE_ASSET_NOTES").SaveX(ctx)
	item := client.Item.Create().SetCompanyID(company).SetName("CLOSED_ITEM").SetDescription("PRIVATE_ITEM_DESCRIPTION").SaveX(ctx)
	var js, ps int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&js))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='project:completed' AND is_category_default`, company).Scan(&ps))
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetName("Visible Project").SetStatusID(ps).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(loc.ID).SetCustomerContactID(contact.ID).SetAssetID(asset.ID).SetProjectID(p.ID).SetJobType("Visible finished work").SetStatusID(js).SaveX(ctx)
	paths := map[string]string{"contact": fmt.Sprintf("/contacts/%d", contact.ID), "location": fmt.Sprintf("/locations/%d", loc.ID), "asset": fmt.Sprintf("/assets/%d", asset.ID), "item": fmt.Sprintf("/items/%d", item.ID)}
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	serviceEntryPost(t, web, adminCookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, asset.ID), url.Values{"service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"PRIVATE_SERVICE_RECORD"}, "responsible_user_id": {fmt.Sprint(admin)}}, 303)
	for _, kind := range []string{"contact", "location", "asset", "item"} {
		fieldPost(t, web, adminCookie, paths[kind]+"/delete", nil, 303)
	}
	svc := authorization.New(pool)
	grants := []authorization.Grant{}
	for _, kind := range []string{"customer", "job", "project", "contact", "location", "asset", "item"} {
		grants = append(grants, authorization.Grant{Permission: kind + ".view", Scope: authorization.All})
	}
	grants = append(grants, authorization.Grant{Permission: "service_entry.view", Scope: authorization.All})
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Source-only viewer", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Source viewer").SetEmail("master-source63@test").SetPasswordHash("x").SetRole("admin").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	check := func() {
		t.Helper()
		for _, source := range []string{fmt.Sprintf("/jobs/%d", j.ID), fmt.Sprintf("/projects/%d", p.ID), fmt.Sprintf("/customers/%d", c.ID), "/search?q=CLOSED", "/contacts?search=CLOSED", "/locations?search=CLOSED", "/assets?search=CLOSED", "/items?search=CLOSED"} {
			body := requestBody(t, web, cookie, "GET", source, 200)
			for _, private := range []string{"PRIVATE_LOCATION_ADDRESS", "PRIVATE_CONTACT_PHONE", "PRIVATE_ASSET_NOTES", "PRIVATE_ITEM_DESCRIPTION", "PRIVATE_SERVICE_RECORD"} {
				assertNotContains(t, body, private)
			}
			for _, target := range paths {
				assertNotContains(t, body, `href="`+target+`"`)
				assertNotContains(t, body, `hx-get="`+target)
			}
			if strings.Contains(source, "?search=") || strings.HasPrefix(source, "/search?") {
				assertNotContains(t, body, "CLOSED_")
			}
		}
		for kind, target := range paths {
			want := 404
			if kind == "asset" {
				want = 403
			}
			expectStatus(t, web, cookie, "GET", target, want)
		}
		for _, target := range []string{paths["asset"] + "/associations", paths["asset"] + "/service-entries", paths["location"] + "/coordinates", fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, asset.ID)} {
			r := httptest.NewRequest("GET", target, nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			web.ServeHTTP(w, r)
			if w.Code != 403 && w.Code != 404 && w.Code != 409 {
				t.Fatalf("source expanded restricted child %s: %d %s", target, w.Code, w.Body.String())
			}
			assertNotContains(t, w.Body.String(), "PRIVATE_")
		}
	}
	check()
	t.Run("persisted-browser", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("set BROWSER_TESTS=1")
		}
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", nosurf.New(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		args := []string{"testdata/browser/master-disclosure.cjs", server.URL, cookie.Value, fmt.Sprintf("/jobs/%d", j.ID), fmt.Sprintf("/projects/%d", p.ID), fmt.Sprintf("/customers/%d", c.ID)}
		for _, kind := range []string{"contact", "location", "asset", "item"} {
			args = append(args, paths[kind])
		}
		output, err := exec.CommandContext(browserCtx, "node", args...).CombinedOutput()
		t.Logf("browser: %s", output)
		historyCheck(t, err)
	})
	for kind := range paths {
		grants = append(grants, authorization.Grant{Permission: kind + ".archived.view", Scope: authorization.All}, authorization.Grant{Permission: kind + ".restore", Scope: authorization.All})
	}
	_, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Independent historical restorer", grants, 1)
	historyCheck(t, err)
	for kind, path := range paths {
		body := requestBody(t, web, cookie, "GET", path, 200)
		if kind == "asset" {
			assertNotContains(t, body, path+"/restore")
		} else {
			assertContains(t, body, path+"/restore")
		}
	}
	fieldPost(t, web, cookie, paths["asset"]+"/restore", nil, 409)
	fieldPost(t, web, cookie, paths["location"]+"/restore", nil, 303)
	assertContains(t, requestBody(t, web, cookie, "GET", paths["asset"], 200), paths["asset"]+"/restore")
	fieldPost(t, web, adminCookie, paths["location"]+"/delete", nil, 303)
	_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND (permission LIKE '%.restore' OR permission LIKE '%.archived.view')`, role)
	historyCheck(t, err)
	check()
}

func TestMasterArchivedLocationReferenceIsBoundedInFieldContext(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Assigned Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Historical place").SetAddress1("PRIVATE_ARCHIVED_ADDRESS").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(l.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(typ.ID).SetName("Active equipment").SaveX(ctx)
	var status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='job:completed' AND is_category_default`, company).Scan(&status))
	var projectStatus int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='project:completed' AND is_category_default`, company).Scan(&projectStatus))
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetLocationID(l.ID).SetStatusID(projectStatus).SetName("Assigned finished project").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetProjectID(p.ID).SetStatusID(status).SetJobType("Assigned finished work").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	fieldPost(t, web, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/locations/%d/delete", l.ID), nil, 303)
	token, err := sessions.CreateMobile(ctx, admin, "Bounded context client")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("context=%d: %s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Project struct {
				Location         string `json:"location"`
				LocationArchived bool   `json:"location_archived"`
			} `json:"project"`
			Assets []struct {
				Location map[string]any `json:"location"`
			} `json:"assets"`
		} `json:"data"`
	}
	historyCheck(t, json.Unmarshal(w.Body.Bytes(), &response))
	if len(response.Data.Assets) != 1 {
		t.Fatalf("missing explicitly linked Asset: %s", w.Body.String())
	}
	ref := response.Data.Assets[0].Location
	if ref["title"] != "Historical place" || ref["archived"] != true || len(ref) != 2 {
		t.Fatalf("archived source reference must contain title/badge only: %#v", ref)
	}
	if response.Data.Project.Location != "Historical place" || !response.Data.Project.LocationArchived {
		t.Fatalf("Project Location lacks historical badge: %s", w.Body.String())
	}
	assertNotContains(t, w.Body.String(), "PRIVATE_ARCHIVED_ADDRESS")
	docResponse := httptest.NewRecorder()
	api.ServeHTTP(docResponse, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc, payload map[string]any
	historyCheck(t, json.Unmarshal(docResponse.Body.Bytes(), &doc))
	historyCheck(t, json.Unmarshal(w.Body.Bytes(), &payload))
	compiler := jsonschema.NewCompiler()
	historyCheck(t, compiler.AddResource("https://master.test/openapi.json", doc))
	for name, value := range map[string]any{"SyncAssetLocation": ref, "SyncProject": payload["data"].(map[string]any)["project"]} {
		schema, err := compiler.Compile("https://master.test/openapi.json#/components/schemas/" + name)
		historyCheck(t, err)
		historyCheck(t, schema.Validate(value))
	}
	t.Run("persisted-reference-browser", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("set BROWSER_TESTS=1")
		}
		mux := http.NewServeMux()
		mux.Handle("/api/v1/", http.StripPrefix("/api/v1", api))
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/master-reference.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value).CombinedOutput()
		t.Logf("reference browser: %s", output)
		historyCheck(t, err)
	})
}

func TestMasterAssetArchivedListAuthorizationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Archive equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("ARCHIVED_ASSET_LIST_63").SaveX(ctx)
	path := fmt.Sprintf("/assets/%d", a.ID)
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	assertNotContains(t, requestBody(t, web, cookie, "GET", "/assets", 200), "ARCHIVED_ASSET_LIST_63")
	body := requestBody(t, web, cookie, "GET", "/assets?archived=1&search=ARCHIVED_ASSET_LIST_63", 200)
	assertContains(t, body, "ARCHIVED_ASSET_LIST_63")
	assertContains(t, body, `href="`+path+`"`)
	assertContains(t, body, `name="archived" value="1"`)
	assertNotContains(t, body, path+"/edit")
	assertNotContains(t, body, path+"/delete")
	t.Run("persisted-browser", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("set BROWSER_TESTS=1")
		}
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", nosurf.New(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/master-asset-list.cjs", server.URL, cookie.Value, path).CombinedOutput()
		t.Logf("browser: %s", output)
		historyCheck(t, err)
	})
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Current Asset viewer", []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Current viewer").SetEmail("archive-list63@test").SetPasswordHash("x").SetRole("admin").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	reader := sessionCookie(t, ctx, sessions, u.ID)
	expectStatus(t, web, reader, "GET", "/assets?archived=1", 403)
	assertNotContains(t, requestBody(t, web, reader, "GET", "/assets", 200), "Archived Assets")
}
