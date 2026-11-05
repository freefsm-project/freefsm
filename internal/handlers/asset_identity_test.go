package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAssetIdentityProductionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	values := url.Values{"name": {"Warehouse pump"}, "ownership_class": {"company_owned"}, "number": {"PUMP-034"}, "asset_type_id": {fmt.Sprint(typ.ID)}, "serial_number": {"repeated"}, "installed_at": {"2024-01-15"}, "warranty_expires": {"2027-01-15"}, "notes": {"Service instructions"}}
	fieldPost(t, router, cookie, "/assets", values, 303)
	list := requestBody(t, router, cookie, "GET", "/assets", 200)
	assertContains(t, list, "PUMP-034")
	a := client.Asset.Query().OnlyX(ctx)
	path := fmt.Sprintf("/assets/%d", a.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	for _, text := range []string{"PUMP-034", "Company-owned", "Warehouse pump", "Service instructions", "2024", "2027"} {
		assertContains(t, body, text)
	}
	values.Set("name", "Duplicate identity")
	fieldPost(t, router, cookie, "/assets", values, 409)
	values.Set("number", "RENUMBERED")
	fieldPost(t, router, cookie, path, values, 409)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "PUMP-034")
}

func TestAssetIdentityPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser owner").SaveX(ctx)
	client.AssetType.Create().SetCompanyID(company).SetName("Browser equipment").SaveX(ctx)
	client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Browser site").SaveX(ctx)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/asset_identity.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	rows := client.Asset.Query().AllX(ctx)
	if len(rows) != 2 {
		t.Fatalf("browser persisted %d Assets", len(rows))
	}
	for _, a := range rows {
		if a.Number == "BROWSER-COMPANY-034" && (a.CustomerID != 0 || a.LocationID != nil || a.OwnershipClass != "company_owned") {
			t.Fatal("browser lost unplaced identity")
		}
	}
}

func TestAssetIdentityValidationConcurrencyAndArchiveHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Owner").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other owner").SaveX(ctx)
	loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(other.ID).SetTitle("Other place").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Pumps").SaveX(ctx)
	values := url.Values{"name": {"Valid pump"}, "ownership_class": {"customer_owned"}, "customer_id": {fmt.Sprint(c.ID)}, "asset_type_id": {fmt.Sprint(typ.ID)}, "number": {"CONCURRENT-034"}, "serial_number": {"repeat"}}
	values.Set("location_id", fmt.Sprint(loc.ID))
	fieldPost(t, router, cookie, "/assets", values, 404)
	values.Del("location_id")
	values.Set("customer_id", "0")
	fieldPost(t, router, cookie, "/assets", values, 409)
	values.Set("ownership_class", "unknown")
	fieldPost(t, router, cookie, "/assets", values, 409)
	values.Set("ownership_class", "customer_owned")
	values.Set("customer_id", fmt.Sprint(c.ID))
	codes := make(chan int, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRequest("POST", "/assets", strings.NewReader(values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			codes <- w.Code
		}()
	}
	wg.Wait()
	first, second := <-codes, <-codes
	if !((first == 303 && second == 409) || (first == 409 && second == 303)) {
		t.Fatalf("duplicate race: %d %d", first, second)
	}
	a := client.Asset.Query().OnlyX(ctx)
	path := fmt.Sprintf("/assets/%d", a.ID)
	values.Set("customer_id", fmt.Sprint(other.ID))
	fieldPost(t, router, cookie, path, values, 409)
	values.Set("customer_id", fmt.Sprint(c.ID))
	values.Set("ownership_class", "company_owned")
	fieldPost(t, router, cookie, path, values, 409)
	fieldPost(t, router, cookie, path+"/delete", nil, 303)
	values.Set("ownership_class", "customer_owned")
	fieldPost(t, router, cookie, "/assets", values, 409)
	values.Set("number", "SECOND-034")
	fieldPost(t, router, cookie, "/assets", values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "CONCURRENT-034")
	fieldPost(t, router, cookie, path+"/restore", nil, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "CONCURRENT-034")
}

func TestAssetIdentityIndependentGrantsAndAtomicAuditHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Equipment registration", []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "asset.create", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("registrar@test").SetName("Registrar").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Hidden Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	values := url.Values{"name": {"Registered equipment"}, "ownership_class": {"company_owned"}, "asset_type_id": {fmt.Sprint(typ.ID)}, "number": {"AUDIT-034"}}
	values.Set("customer_id", fmt.Sprint(c.ID))
	fieldPost(t, router, cookie, "/assets", values, 404)
	values.Del("customer_id")
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_asset_identity_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='asset.create' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_asset_identity_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_asset_identity_audit()`); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, "/assets", values, 500)
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/assets", 200), "AUDIT-034")
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_asset_identity_audit ON authorization_audit; DROP FUNCTION reject_asset_identity_audit()`); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, "/assets", values, 303)
	a := client.Asset.Query().OnlyX(ctx)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d", a.ID), values, 403)
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE actor_id=$1 AND role_id=$2 AND target_id=$3 AND action='asset.create' AND scope='A' AND detail->'after'->>'number'='AUDIT-034'`, u.ID, role, a.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("atomic identity audit %d %v", count, err)
	}
	grants := []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "asset.archived.view", Scope: authorization.All}, {Permission: "asset.archive", Scope: authorization.All}, {Permission: "asset.restore", Scope: authorization.All}}
	if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Equipment lifecycle", grants, 1); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/delete", a.ID), nil, 303)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/restore", a.ID), nil, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", a.ID), 200), "AUDIT-034")
}

func TestAssetCompanyPlacementCannotBeArchived(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Placed Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Placed equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("company_owned").SetName("Placed pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/delete", a.ID), nil, 409)
	assertNotContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", a.ID), 200), "Archived on")
}

func TestAssetRetainedGlobalVocabularyVisibleHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	typ := client.AssetType.Create().SetName("Retained global Type").SaveX(ctx)
	client.AssetType.Create().SetCompanyID(company + 1000).SetName("FOREIGN_VOCABULARY").SaveX(ctx)
	status := client.AssetStatus.Create().SetName("Retained global Status").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetName("Historical equipment").SetAssetTypeID(typ.ID).SetAssetStatusID(status.ID).SaveX(ctx)
	client.AssetType.UpdateOneID(typ.ID).SetRetired(true).SaveX(ctx)
	client.AssetStatus.UpdateOneID(status.ID).SetRetired(true).SaveX(ctx)
	path := fmt.Sprintf("/assets/%d", a.ID)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Retained global Type")
	assertContains(t, body, "Retained global Status")
	list := requestBody(t, router, cookie, "GET", "/assets", 200)
	assertContains(t, list, "Retained global Type")
	assertContains(t, list, "Retained global Status")
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/assets/new", 200), "FOREIGN_VOCABULARY")
	fieldPost(t, router, cookie, path, url.Values{"name": {"Unrelated retained edit"}}, 303)
	body = requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Retained global Type")
	assertContains(t, body, "Retained global Status")
}

func TestAssetCustomerCascadePreservesOwnershipClassesHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Cascade owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Cascade equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetNumber("CASCADE-034").SetName("Owned pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	unplaced := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetName("Warehouse pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	var preview authorization.CustomerArchivePreview
	masterJSON(t, router, cookie, base+"/archive", &preview)
	if len(preview.Children) != 1 || preview.Children[0].Kind != "asset" || preview.Children[0].ID != a.ID {
		t.Fatalf("incorrect Asset cascade preview: %+v", preview)
	}
	masterPost(t, router, cookie, base+"/delete", url.Values{"archive_snapshot": {preview.Snapshot}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", a.ID), 200), "Archived on")
	assertNotContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", unplaced.ID), 200), "Archived on")
	masterPost(t, router, cookie, base+"/restore", nil, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", a.ID), 200), "Archived on")
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/restore", a.ID), nil, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", a.ID), 200), "CASCADE-034")
}

func TestAssetCreationRechecksWaitingMasterChangesHTTP(t *testing.T) {
	for _, kind := range []string{"customer", "location", "type"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race owner").SaveX(ctx)
			loc := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Race location").SaveX(ctx)
			typ := client.AssetType.Create().SetCompanyID(company).SetName("Race equipment").SaveX(ctx)
			values := url.Values{"name": {"Must not publish"}, "number": {"RACE-034"}, "ownership_class": {"customer_owned"}, "customer_id": {fmt.Sprint(c.ID)}, "location_id": {fmt.Sprint(loc.ID)}, "asset_type_id": {fmt.Sprint(typ.ID)}}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var pid int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) fence`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() {
				r := httptest.NewRequest("POST", "/assets", strings.NewReader(values.Encode()))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				done <- w.Code
			}()
			deadline := time.Now().Add(10 * time.Second)
			for {
				var waiting bool
				if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("Asset creation did not reach authority fence")
				}
				time.Sleep(10 * time.Millisecond)
			}
			statement := map[string]string{"customer": `UPDATE customers SET deleted_at=now() WHERE id=$1`, "location": `UPDATE locations SET deleted_at=now() WHERE id=$1`, "type": `UPDATE asset_types SET retired=true WHERE id=$1`}[kind]
			id := map[string]int64{"customer": c.ID, "location": loc.ID, "type": typ.ID}[kind]
			if _, err = tx.Exec(ctx, statement, id); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			code := <-done
			if code != 404 && code != 409 {
				t.Fatalf("racing %s: %d", kind, code)
			}
			assertNotContains(t, requestBody(t, router, cookie, "GET", "/assets", 200), "RACE-034")
			var count int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='asset.create'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed publication audit %d %v", count, err)
			}
		})
	}
}

func TestAssetIdentityAPIContext(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Asset context Customer").SaveX(ctx)
	p := client.Project.Create().SetCompanyID(company).SetCustomerID(c.ID).SetName("Linked operational Project").SetDescription("Allowed Project description").SetServiceNotes("Allowed Project instructions").SetNotes("SECRET_PROJECT_NOTES").SetCustomFields(`{"private":"SECRET_PROJECT_FIELD"}`).SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Context equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetNumber("FIELD-034").SetName("Linked equipment").SetAssetTypeID(typ.ID).SetNotes("Allowed service instructions").SetCustomFields(`{"private":"SECRET_MASTER"}`).SaveX(ctx)
	client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("SECRET_SIBLING").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetProjectID(p.ID).SetAssetID(a.ID).SetJobType("Field job").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetEmail("asset-field@test").SetName("Field worker").SetPasswordHash("hash").SetRole("dispatcher").SaveX(ctx)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Customer and field context", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetUserID(u.ID).SetJobID(j.ID).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, u.ID, "Asset context device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	get := func(want int) string {
		t.Helper()
		r := httptest.NewRequest("GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("context: %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	before := time.Now()
	body := get(200)
	after := time.Now()
	var envelope struct {
		Data struct {
			Asset map[string]json.RawMessage `json:"asset"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	projection := envelope.Data
	allowed := strings.Fields("id number name ownership_class type status serial_number manufacturer model installed_at warranty_expires location service_notes lease_coverage")
	if len(projection.Asset) != len(allowed) {
		t.Fatalf("Asset context allowlist changed: %s", body)
	}
	for _, key := range allowed {
		if _, ok := projection.Asset[key]; !ok {
			t.Fatalf("missing Asset context field %s", key)
		}
	}
	var coverage map[string]json.RawMessage
	if err := json.Unmarshal(projection.Asset["lease_coverage"], &coverage); err != nil {
		t.Fatal(err)
	}
	if len(coverage) != 2 || string(coverage["covered"]) != "false" {
		t.Fatalf("expected bounded uncovered observation: %s", projection.Asset["lease_coverage"])
	}
	var asOf time.Time
	if err := json.Unmarshal(coverage["as_of"], &asOf); err != nil || asOf.Before(before) || asOf.After(after) {
		t.Fatalf("expected current observation time, got %s: %v", coverage["as_of"], err)
	}
	for _, method := range []string{"GET", "POST"} {
		r := httptest.NewRequest(method, fmt.Sprintf("/assets/%d", a.ID), nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("field API exposed master %s: %d", method, w.Code)
		}
	}
	for _, text := range []string{"FIELD-034", "customer_owned", "Linked equipment", "Allowed service instructions"} {
		assertContains(t, body, text)
	}
	assertNotContains(t, body, "SECRET_")
	assertNotContains(t, body, "Linked operational Project")
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/assets/options", c.ID), 403)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), 200), "FIELD-034")
	expectStatus(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", a.ID), 403)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d", a.ID), url.Values{"name": {"Forbidden"}}, 404)
	// Both linked operational projections must coexist, with independent grants.
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.Assigned}, {Permission: "project.view", Scope: authorization.Context}, {Permission: "asset.view", Scope: authorization.Context}}
	if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Customer and field context", grants, 1); err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{
		get(200),
		requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), 200),
		requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d/customer-context", j.ID), 200),
	} {
		for _, text := range []string{"FIELD-034", "Linked operational Project", "Allowed Project description", "Allowed Project instructions"} {
			assertContains(t, response, text)
		}
		assertNotContains(t, response, "SECRET_")
	}
	if _, err = svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Customer and field context", grants[:3], 2); err != nil {
		t.Fatal(err)
	}
	for _, response := range []string{get(200), requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), 200)} {
		assertContains(t, response, "Linked operational Project")
		assertNotContains(t, response, "FIELD-034")
		assertNotContains(t, response, "SECRET_")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	get(404)
}
