package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestLeaseLifecycleAndRegistrationPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser registration Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Private premises name").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Private intended Asset name").SaveX(ctx)
	field := client.CustomFieldDefinition.Create().SetCompanyID(company).SetObjectType("lease").SetName("Required after minimal registration").SetFieldType("text").SetRequired(true).SaveX(ctx)
	fieldPost(t, web, adminCookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, adminCookie, path, url.Values{"revision": {"0"}, "title": {"Browser base-only history"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(asset.ID)}, fmt.Sprintf("custom_field_%d", field.ID): {"Preserved required answer"}}, 303)
	fieldPost(t, web, adminCookie, path+"/billing", url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(asset.ID)}, "rent_cents": {"5412345"}}, 303)
	cookies := []string{}
	for _, roleCase := range []struct {
		name string
		keys []string
	}{
		{"creator", []string{"lease.view", "lease.manage", "lease.archived.view", "customer.view", "file.upload"}},
		{"lifecycle", []string{"lease.view", "lease.archived.view", "lease.archive", "lease.restore", "location.view", "location.archive", "asset.view", "asset.archive"}},
		{"billing", []string{"lease.billing.view", "lease.billing.manage", "lease.archived.view"}},
	} {
		grants := []authorization.Grant{}
		for _, key := range roleCase.keys {
			grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
		}
		svc := authorization.New(pool)
		role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lease browser "+roleCase.name, grants)
		historyCheck(t, err)
		u := client.User.Create().SetCompanyID(company).SetName(roleCase.name).SetEmail(roleCase.name + "@lifecycle65.test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
		cookies = append(cookies, sessionCookie(t, ctx, sessions, u.ID).Value)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-lifecycle.cjs", server.URL, cookies[0], cookies[1], cookies[2], fmt.Sprint(id), fmt.Sprint(c.ID), fmt.Sprint(l.ID), fmt.Sprint(asset.ID)).CombinedOutput()
	t.Logf("Chromium lifecycle: %s", output)
	historyCheck(t, err)
	var safe bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_asset_dependencies) AND NOT EXISTS(SELECT 1 FROM invoices) AND EXISTS(SELECT 1 FROM locations WHERE id=$1 AND deleted_at IS NOT NULL) AND EXISTS(SELECT 1 FROM assets WHERE id=$2 AND deleted_at IS NOT NULL AND customer_id IS NULL)`, l.ID, asset.ID).Scan(&safe))
	if !safe {
		t.Fatal("browser restore revived operational effects")
	}
}
