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
	"path/filepath"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func serviceEntryPost(t *testing.T, router http.Handler, cookie *http.Cookie, path string, form url.Values, want int) {
	t.Helper()
	if !form.Has("link_id") {
		root := path
		if index := strings.Index(root, "/service-entries"); index >= 0 {
			root = root[:index] + "/service-entries"
		}
		r := httptest.NewRequest("GET", root+"?format=json", nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code == 200 {
			form.Set("link_id", w.Header().Get("Service-Relationship-ID"))
		}
	}
	copy := url.Values{}
	for key, values := range form {
		copy[key] = append([]string(nil), values...)
	}
	if copy.Get("operation_id") == "" {
		copy.Set("operation_id", newOperationKey())
	}
	fieldPost(t, router, cookie, path, copy, want)
}

func TestServiceEntryFieldAPIAndOfflineReference(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("API service").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Service reference")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "node", "testdata/browser/service-entry-reference.cjs", server.URL, token, fmt.Sprint(j.ID), fmt.Sprint(a.ID), fmt.Sprint(admin), time.Now().UTC().Format("2006-01-02"), filepath.Join(t.TempDir(), "service-pending.json")).CombinedOutput()
	t.Logf("reference: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceEntryCreatePreservesProvenanceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Service owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Pump service").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	form := url.Values{"service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"Replaced seal"}, "responsible_user_id": {fmt.Sprint(admin)}}
	serviceEntryPost(t, router, cookie, path, form, 303)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Replaced seal")
	assetPage := requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d", a.ID), 200)
	assertContains(t, assetPage, fmt.Sprintf("/assets/%d/service-entries", a.ID))
	assertContains(t, assetPage, "Linked Job history")
	assertContains(t, assetPage, "Pump service")
	assertNotContains(t, assetPage, "<h3>Service History</h3>")
	var entries, blockers, audits int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM asset_service_entries WHERE job_id=$1),(SELECT count(*) FROM job_customer_blockers WHERE job_id=$1),(SELECT count(*) FROM authorization_audit WHERE action='service_entry.create')`, j.ID).Scan(&entries, &blockers, &audits); err != nil || entries != 1 || blockers != 1 || audits != 1 {
		t.Fatalf("entries=%d blockers=%d audits=%d err=%v", entries, blockers, audits, err)
	}
}

func TestServiceEntryCreateOnceReceiptHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Replay Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Replay service").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	form := url.Values{"operation_id": {"42000000-0000-4000-8000-000000000001"}, "service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"Never duplicate"}, "responsible_user_id": {fmt.Sprint(admin)}}
	serviceEntryPost(t, router, cookie, path, form, 303)
	serviceEntryPost(t, router, cookie, path, form, 303)
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_entries WHERE job_id=$1`, j.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("replay created %d entries err=%v", n, err)
	}
	form.Set("summary", "Changed intent")
	serviceEntryPost(t, router, cookie, path, form, 409)
	body := requestBody(t, router, cookie, "GET", "/service-entry-operations/42000000-0000-4000-8000-000000000001", 200)
	assertContains(t, body, `"code":"accepted"`)
	assertNotContains(t, body, "Never duplicate")
	commentPost(t, router, cookie, fmt.Sprintf("/jobs/%d/comments", j.ID), url.Values{"operation_id": {form.Get("operation_id")}, "content": {"Cross-family UUID reuse"}}, 409)
}

func TestServiceEntryPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Browser service").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "node", "testdata/browser/service_entries.cjs", server.URL, cookie.Value, fmt.Sprint(j.ID), fmt.Sprint(a.ID), fmt.Sprint(admin), time.Now().UTC().Format("2006-01-02")).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceEntryAssignedAuthorityAndTransferRaceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "asset.view", Scope: authorization.Context}, {Permission: "service_entry.view", Scope: authorization.Assigned}, {Permission: "service_entry.create", Scope: authorization.Assigned}, {Permission: "service_entry.revise", Scope: authorization.Assigned}}
	role, err := auth.SaveRole(ctx, actor, 0, "Service worker", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("service-worker@test").SetName("Worker").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err = auth.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original Customer").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Next Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Assigned service").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	today := time.Now().UTC().Format("2006-01-02")
	form := url.Values{"service_date": {today}, "summary": {"Assigned service"}, "responsible_user_id": {fmt.Sprint(u.ID)}}
	serviceEntryPost(t, router, cookie, path, form, 404)
	if _, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	form.Set("operation_id", newOperationKey())
	serviceEntryPost(t, router, cookie, path, form, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", fmt.Sprintf("/jobs/%d/master-context", j.ID), 200), "Assigned service")
	// An independent missing Asset context grant must not be bypassed by a
	// Service Entry grant or a known operational Asset ID.
	if _, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='asset.view'`, role); err != nil {
		t.Fatal(err)
	}
	receipt := requestBody(t, router, cookie, "GET", "/service-entry-operations/"+form.Get("operation_id"), 200)
	assertContains(t, receipt, `"code":"accepted"`)
	assertNotContains(t, receipt, "Assigned service")
	serviceEntryPost(t, router, cookie, path, form, 303)
	form.Del("operation_id")
	serviceEntryPost(t, router, cookie, path, form, 404)
	if _, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'asset.view','C')`, role); err != nil {
		t.Fatal(err)
	}
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	form.Set("operation_id", newOperationKey())
	start := make(chan struct{})
	done := make(chan int, 2)
	for _, req := range []struct {
		path   string
		values url.Values
		cookie *http.Cookie
	}{{path, form, cookie}, {fmt.Sprintf("/assets/%d/associations/transfer", a.ID), url.Values{"customer_id": {fmt.Sprint(next.ID)}, "effective_date": {today}, "revision": {"0"}, "reason": {"Sale"}}, adminCookie}} {
		go func() {
			<-start
			r := httptest.NewRequest("POST", req.path, strings.NewReader(req.values.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(req.cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			done <- w.Code
		}()
	}
	close(start)
	for range 2 {
		if code := <-done; code != 303 && code != 409 {
			t.Fatalf("race status=%d", code)
		}
	}
	var invalid int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_entries e JOIN jobs j ON j.id=e.job_id WHERE e.customer_id<>j.customer_id OR e.customer_id<>$1`, c.ID).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatalf("invalid provenance=%d err=%v", invalid, err)
	}
	form.Del("operation_id")
	serviceEntryPost(t, router, cookie, path, form, 409)
}

func TestServiceEntryAssociationCorrectionCannotRewriteProvenanceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recorded Customer").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Service").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	today := time.Now().UTC().Format("2006-01-02")
	serviceEntryPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID), url.Values{"service_date": {today}, "summary": {"Recorded service"}, "responsible_user_id": {fmt.Sprint(admin)}}, 303)
	var link int64
	if err := pool.QueryRow(ctx, `SELECT id FROM job_asset_links WHERE job_id=$1`, j.ID).Scan(&link); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"unlink"}, "link_id": {fmt.Sprint(link)}}, 303)
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"link"}, "asset_id": {fmt.Sprint(a.ID)}}, 303)
	serviceEntryPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID), url.Values{"link_id": {fmt.Sprint(link)}, "service_date": {today}, "summary": {"Queued against lost original relationship"}, "responsible_user_id": {fmt.Sprint(admin)}}, 409)
	if err := pool.QueryRow(ctx, `SELECT id FROM job_asset_links WHERE job_id=$1 AND operational AND unlinked_at IS NULL`, j.ID).Scan(&link); err != nil {
		t.Fatal(err)
	}
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets", j.ID), url.Values{"action": {"unlink"}, "link_id": {fmt.Sprint(link)}}, 303)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/associations/correct", a.ID), url.Values{"event_id": {"0"}, "customer_id": {fmt.Sprint(next.ID)}, "effective_date": {today}, "reason": {"Cannot falsify service provenance"}, "revision": {"0"}}, 409)
}

func TestServiceEntryAuditRollbackAndCustomerRaceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original Customer").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Replacement Customer").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Atomic service").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	form := url.Values{"operation_id": {newOperationKey()}, "service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"Atomic service"}, "responsible_user_id": {fmt.Sprint(admin)}}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_service_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service_entry.create' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='injected service audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_service_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_service_audit()`); err != nil {
		t.Fatal(err)
	}
	serviceEntryPost(t, router, cookie, path, form, 503)
	var partial int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM asset_service_entries)+(SELECT count(*) FROM job_customer_blockers)+(SELECT count(*) FROM service_entry_operation_receipts)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)`, form.Get("operation_id")).Scan(&partial); err != nil || partial != 0 {
		t.Fatalf("audit rollback partial=%d err=%v", partial, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_service_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	planningPath := fmt.Sprintf("/jobs/%d", j.ID)
	planning := currentJobFormValues(t, router, cookie, planningPath, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "job_type": {"Atomic service"}, "customer_id": {fmt.Sprint(next.ID)}, "replace_customer": {"true"}})
	start := make(chan struct{})
	done := make(chan int, 2)
	for _, request := range []struct {
		path string
		form url.Values
	}{{path, form}, {planningPath, planning}} {
		go func() {
			<-start
			r := httptest.NewRequest("POST", request.path, strings.NewReader(request.form.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			done <- w.Code
		}()
	}
	close(start)
	accepted := 0
	for range 2 {
		code := <-done
		if code == 303 {
			accepted++
		} else if code != 409 {
			t.Fatalf("race status=%d", code)
		}
	}
	if accepted != 1 {
		t.Fatalf("incompatible Customer replacement and first service accepted=%d", accepted)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_entries e JOIN jobs j ON j.id=e.job_id WHERE e.customer_id<>j.customer_id`).Scan(&partial); err != nil || partial != 0 {
		t.Fatalf("invalid Customer provenance=%d err=%v", partial, err)
	}
}

func TestServiceEntryRevisionRejectsABAAndRetainsIdentityHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	responsible := client.User.Create().SetCompanyID(company).SetEmail("responsible42@test").SetName("Original responsible").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Responsible user", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, responsible.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Revision owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(a.ID).SetJobType("Pump service").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	form := url.Values{"service_date": {time.Now().UTC().Format("2006-01-02")}, "summary": {"Original"}, "responsible_user_id": {fmt.Sprint(responsible.ID)}}
	serviceEntryPost(t, router, cookie, path, form, 303)
	read := func() authorization.ServiceEntry {
		raw := requestBody(t, router, cookie, "GET", path+"?format=json", 200)
		var entries []authorization.ServiceEntry
		if err := json.Unmarshal([]byte(raw), &entries); err != nil || len(entries) != 1 {
			t.Fatalf("history: %s err=%v", raw, err)
		}
		return entries[0]
	}
	first := read()
	form.Set("revision", first.Revision)
	form.Set("summary", "Corrected")
	revise := fmt.Sprintf("%s/%d", path, first.ID)
	serviceEntryPost(t, router, cookie, revise, form, 303)
	second := read()
	form.Set("revision", second.Revision)
	form.Set("summary", "Original")
	serviceEntryPost(t, router, cookie, revise, form, 303)
	form.Set("revision", first.Revision)
	form.Set("summary", "Stale overwriting text")
	serviceEntryPost(t, router, cookie, revise, form, 409)
	if _, err := pool.Exec(ctx, `UPDATE users SET name='Renamed responsible',is_active=false WHERE id=$1`, responsible.ID); err != nil {
		t.Fatal(err)
	}
	current := read()
	if current.ID != first.ID || current.Revision == first.Revision || current.Summary != "Original" || len(current.ResponsibleNames) != 1 || current.ResponsibleNames[0] != "Renamed responsible" {
		t.Fatalf("lost identity/revision: %+v", current)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM asset_service_revisions WHERE entry_id=$1`, first.ID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("immutable history n=%d err=%v", n, err)
	}
	history := requestBody(t, router, cookie, "GET", revise+"/revisions", 200)
	assertContains(t, history, "Original")
	assertContains(t, history, "Corrected")
	assertContains(t, history, "Admin")
}

func TestServiceEntryLifetimeAndCustomerHistoryAfterTransferHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	old := client.Customer.Create().SetCompanyID(company).SetDisplayName("Old owner").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("New owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetCustomerID(old.ID).SetOwnershipClass("customer_owned").SetName("Pump").SetAssetTypeID(typ.ID).SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(old.ID).SetAssetID(a.ID).SetJobType("Old owner service").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/assets/%d/service-entries", j.ID, a.ID)
	today := time.Now().UTC().Format("2006-01-02")
	form := url.Values{"service_date": {today}, "summary": {"Original private service"}, "responsible_user_id": {fmt.Sprint(admin)}}
	serviceEntryPost(t, router, cookie, path, form, 303)
	fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d/associations/transfer", a.ID), url.Values{"customer_id": {fmt.Sprint(next.ID)}, "effective_date": {today}, "reason": {"Sale"}, "revision": {"0"}}, 303)
	serviceEntryPost(t, router, cookie, path, form, 409)
	newJob := client.Job.Create().SetCompanyID(company).SetCustomerID(next.ID).SetAssetID(a.ID).SetJobType("New owner service").SaveX(ctx)
	form.Del("link_id") // A separately reviewed new Job relationship, not replay.
	form.Set("summary", "New owner private service")
	serviceEntryPost(t, router, cookie, fmt.Sprintf("/jobs/%d/assets/%d/service-entries", newJob.ID, a.ID), form, 303)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/assets/%d/service-entries", a.ID), 200)
	assertContains(t, body, "Original private service")
	assertContains(t, body, "New owner private service")
	for _, owner := range []struct {
		id                 int64
		included, excluded string
	}{{old.ID, "Original private service", "New owner private service"}, {next.ID, "New owner private service", "Original private service"}} {
		body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/customers/%d/assets/%d/service-entries", owner.id, a.ID), 200)
		assertContains(t, body, owner.included)
		assertNotContains(t, body, owner.excluded)
	}
	// Even after departure, Customer reassignment cannot erase the original
	// service provenance. This exercises the actual planning route.
	masterPost(t, router, cookie, fmt.Sprintf("/jobs/%d", j.ID), url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "job_type": {"Old owner service"}, "customer_id": {fmt.Sprint(next.ID)}, "replace_customer": {"true"}}, 409)
}
