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

func TestLeaseDetailsPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser Lease Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Browser premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Browser equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Browser intended Asset").SaveX(ctx)
	field := client.CustomFieldDefinition.Create().SetCompanyID(company).SetObjectType("lease").SetName("Browser agreement notes").SetFieldType("textarea").SetRequired(true).SaveX(ctx)
	tag := client.Tag.Create().SetCompanyID(company).SetName("Browser lease tag").SaveX(ctx)
	fieldPost(t, router, sessionCookie(t, ctx, sessions, admin), "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	cookies := []string{}
	for _, facet := range []string{"base", "billing", "reader"} {
		keys := []string{"lease.view", "lease.manage", "location.view", "asset.view", "comment.add", "file.upload", "file.download", "tag.attach", "tag.detach"}
		if facet == "billing" {
			keys = []string{"lease.billing.view", "lease.billing.manage"}
		}
		if facet == "reader" {
			keys = []string{"lease.view", "lease.billing.view"}
		}
		grants := []authorization.Grant{}
		for _, key := range keys {
			grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
		}
		svc := authorization.New(pool)
		role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Browser Lease "+facet, grants)
		historyCheck(t, err)
		u := client.User.Create().SetCompanyID(company).SetName(facet).SetEmail("browser-lease-" + facet + "@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
		cookies = append(cookies, sessionCookie(t, ctx, sessions, u.ID).Value)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browserCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
	defer cancel()
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-details.cjs", server.URL, cookies[0], cookies[1], cookies[2], fmt.Sprint(id), fmt.Sprint(l.ID), fmt.Sprint(a.ID), fmt.Sprint(field.ID), fmt.Sprint(tag.ID)).CombinedOutput()
	t.Logf("persisted Chromium: %s", output)
	if err != nil {
		t.Fatal(err)
	}
	var clean bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM invoices) AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies) AND EXISTS(SELECT 1 FROM assets WHERE id=$1 AND customer_id IS NULL)`, a.ID).Scan(&clean))
	if !clean {
		t.Fatal("browser Draft work had operational effects")
	}
}
