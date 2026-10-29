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

func TestLeaseFinalizationReviewBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("BROWSER_TESTS=1 required")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser finalization").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Unplaced").SaveX(ctx)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	date := today.Format(time.DateOnly)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {date}, "ends_on": {today.AddDate(1, 0, 0).Format(time.DateOnly)}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	amount := int64(12345)
	svc := authorization.New(pool)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id, 0, date, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &amount, RentCents: &amount, TaxRate: "0"}, 0))
	cookies := []string{}
	for i, keys := range [][]string{{"lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage"}, {"lease.view", "lease.manage"}} {
		grants := []authorization.Grant{}
		for _, key := range keys {
			grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
		}
		role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, fmt.Sprintf("Browser finalizer %d", i), grants)
		historyCheck(t, err)
		u := client.User.Create().SetCompanyID(company).SetName("Finalizer").SetEmail(fmt.Sprintf("finalizer%d@browser66.test", i)).SetPasswordHash("x").SetRole("tech").SaveX(ctx)
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
	output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-finalization.cjs", server.URL, cookies[0], cookies[1], fmt.Sprint(id), date).CombinedOutput()
	t.Logf("Chromium finalization: %s", output)
	historyCheck(t, err)
	var valid bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM lease_finalizations)=1 AND (SELECT count(*) FROM authorization_audit WHERE action='lease.finalize')=1 AND NOT EXISTS(SELECT 1 FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE s.category_key<>'invoice:draft') AND EXISTS(SELECT 1 FROM assets WHERE id=$1 AND customer_id IS NULL)`, a.ID).Scan(&valid))
	if !valid {
		t.Fatal("browser replay duplicated finalization or moved equipment")
	}
}
