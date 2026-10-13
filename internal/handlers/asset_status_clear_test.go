package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestAssetStatusClearProductionHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Status owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Status type").SaveX(ctx)
	status := client.AssetStatus.Create().SetCompanyID(company).SetName("Clearable status").SaveX(ctx)
	a := client.Asset.Create().SetOwnershipClass("customer_owned").SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetAssetStatusID(status.ID).SetName("Status asset").SaveX(ctx)
	path := fmt.Sprintf("/assets/%d", a.ID)
	values := url.Values{"name": {"Cleared asset"}, "customer_id": {fmt.Sprint(c.ID)}, "asset_type_id": {fmt.Sprint(typ.ID)}, "asset_status_id": {"0"}}
	fieldPost(t, router, cookie, path, values, 303)
	body := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, body, "Cleared asset")
	assertContains(t, body, "No Status")
	assertNotContains(t, body, "Clearable status")
	if got := client.Asset.GetX(ctx, a.ID); got.AssetStatusID != nil {
		t.Fatalf("clear did not persist: status=%d", *got.AssetStatusID)
	}
}

func TestAssetStatusRetiredPreservationAndReselectionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retired status owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Retired type").SaveX(ctx)
	status := client.AssetStatus.Create().SetCompanyID(company).SetName("Historical status").SaveX(ctx)
	active := client.AssetStatus.Create().SetCompanyID(company).SetName("Active replacement").SaveX(ctx)
	a := client.Asset.Create().SetOwnershipClass("customer_owned").SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetAssetStatusID(status.ID).SetName("Original asset").SaveX(ctx)
	path := fmt.Sprintf("/assets/%d", a.ID)
	for _, route := range []string{fmt.Sprintf("/settings/asset-types/%d/retire", typ.ID), fmt.Sprintf("/settings/asset-statuses/%d/retire", status.ID)} {
		expectStatus(t, router, cookie, "POST", route, 303)
	}
	values := url.Values{"name": {"Unrelated edit"}, "customer_id": {fmt.Sprint(c.ID)}, "asset_type_id": {fmt.Sprint(typ.ID)}}
	// Both omission and an unchanged submitted retired value retain the reference.
	for _, explicit := range []bool{false, true} {
		if explicit {
			values.Set("asset_status_id", fmt.Sprint(status.ID))
		}
		fieldPost(t, router, cookie, path, values, 303)
		assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Historical status")
	}
	values.Set("asset_status_id", "0")
	fieldPost(t, router, cookie, path, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "No Status")
	assertNotContains(t, requestBody(t, router, cookie, "GET", path+"/edit", 200), "Historical status")
	got := client.Asset.GetX(ctx, a.ID)
	if got.AssetStatusID != nil || got.AssetTypeID != typ.ID || got.CustomerID != c.ID || got.DeletedAt != nil {
		t.Fatalf("clear changed unrelated identity/lifecycle: %+v", got)
	}
	if !client.AssetStatus.GetX(ctx, status.ID).Retired || !client.AssetType.GetX(ctx, typ.ID).Retired {
		t.Fatal("clear reactivated a definition")
	}
	var history int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='asset_status' AND target_id=$1 AND action='configuration.assets.manage'`, status.ID).Scan(&history); err != nil || history != 1 {
		t.Fatalf("retirement history lost: %d %v", history, err)
	}
	values.Set("asset_status_id", fmt.Sprint(status.ID))
	values.Set("name", "Rejected retired reselection")
	fieldPost(t, router, cookie, path, values, 409)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Unrelated edit")
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "No Status")
	values.Set("asset_status_id", fmt.Sprint(active.ID))
	values.Set("name", "Active selection")
	fieldPost(t, router, cookie, path, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Active replacement")
	values.Del("asset_status_id")
	fieldPost(t, router, cookie, path, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Active replacement")
}

func TestAssetStatusClearAuthorizationAndAuditRollbackHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "asset.view", Scope: authorization.All}, {Permission: "asset.edit", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Asset editor", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("status-editor@test").SetName("Editor").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Atomic type").SaveX(ctx)
	status := client.AssetStatus.Create().SetCompanyID(company).SetName("Atomic status").SaveX(ctx)
	a := client.Asset.Create().SetOwnershipClass("customer_owned").SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetAssetStatusID(status.ID).SetName("Atomic asset").SaveX(ctx)
	path := fmt.Sprintf("/assets/%d", a.ID)
	values := url.Values{"name": {"Cleared atomically"}, "customer_id": {fmt.Sprint(c.ID)}, "asset_type_id": {fmt.Sprint(typ.ID)}, "asset_status_id": {"0"}}
	assertUnchanged := func() {
		t.Helper()
		got := client.Asset.GetX(ctx, a.ID)
		if got.AssetStatusID == nil || *got.AssetStatusID != status.ID || got.Name != "Atomic asset" {
			t.Fatalf("failed clear partially persisted: %+v", got)
		}
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='asset' AND target_id=$1 AND action='asset.edit'`, a.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed clear recorded success: %d %v", count, err)
		}
		assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Atomic status")
	}
	// Fail the protected sink after the real Asset update, then verify both fields roll back.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_asset_clear_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='asset.edit' THEN RAISE EXCEPTION 'injected asset audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_asset_clear_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_asset_clear_audit()`); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, path, values, 500)
	assertUnchanged()
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_asset_clear_audit ON authorization_audit; DROP FUNCTION reject_asset_clear_audit()`); err != nil {
		t.Fatal(err)
	}
	// Revoke while the production request waits at the commit-time authority fence.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		done <- w.Code
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("clear did not reach authority fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='asset.edit'`, role); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 403 {
		t.Fatalf("revoked waiting clear: got %d want 403", code)
	}
	fieldPost(t, router, cookie, path, values, 403)
	assertUnchanged()
	if _, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Asset editor", grants, 1); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, path, values, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "No Status")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE actor_id=$1 AND role_id=$2 AND action='asset.edit' AND scope='A' AND target_type='asset' AND target_id=$3 AND channel='web' AND outcome='success' AND recorded_at IS NOT NULL`, u.ID, role, a.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing attributable protected clear audit: %d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE authorization_audit SET detail='{}' WHERE action='asset.edit' AND target_id=$1`, a.ID); err == nil {
		t.Fatal("clear audit was mutable")
	}
}

func TestAssetStatusClearPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser status owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Browser status type").SaveX(ctx)
	status := client.AssetStatus.Create().SetCompanyID(company).SetName("Browser clearable status").SaveX(ctx)
	a := client.Asset.Create().SetOwnershipClass("customer_owned").SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetAssetStatusID(status.ID).SetName("Browser status asset").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/asset_status_clear.cjs", server.URL, cookie.Value, fmt.Sprint(a.ID), fmt.Sprint(status.ID)).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	if got := client.Asset.GetX(ctx, a.ID); got.AssetStatusID != nil {
		t.Fatalf("browser clear did not persist: status=%d", *got.AssetStatusID)
	}
}

func TestAssetStatusInvalidIsNotClearHTTP(t *testing.T) {
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Invalid status owner").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Invalid status type").SaveX(ctx)
	status := client.AssetStatus.Create().SetCompanyID(company).SetName("Retained status").SaveX(ctx)
	a := client.Asset.Create().SetOwnershipClass("customer_owned").SetCompanyID(company).SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetAssetStatusID(status.ID).SetName("Retained asset").SaveX(ctx)
	for _, invalid := range []string{"garbage", "", "-1", "9223372036854775808"} {
		t.Run(fmt.Sprintf("status=%q", invalid), func(t *testing.T) {
			fieldPost(t, router, cookie, fmt.Sprintf("/assets/%d", a.ID), url.Values{"name": {"Must roll back"}, "customer_id": {fmt.Sprint(c.ID)}, "asset_type_id": {fmt.Sprint(typ.ID)}, "asset_status_id": {invalid}}, 400)
			got := client.Asset.GetX(ctx, a.ID)
			if got.AssetStatusID == nil || *got.AssetStatusID != status.ID || got.Name != "Retained asset" {
				t.Fatalf("invalid status mutated asset: %+v", got)
			}
		})
	}
}
