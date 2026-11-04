package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestLeaseWorkspaceAssetCoveragePagesHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Coverage customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Coverage premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Coverage equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Coverage asset").SaveX(ctx)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	var archivedID int64
	for i := 0; i < 28; i++ {
		start := today.AddDate(0, 0, -100+i*3)
		title := fmt.Sprintf("Historical coverage %02d", i)
		if i == 27 {
			start, title = today.AddDate(0, 0, -1), "Newest current coverage"
		}
		lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
		historyCheck(t, err)
		historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: title, StartsOn: start.Format(time.DateOnly), EndsOn: start.AddDate(1, 0, 0).Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
		zero, rent := int64(0), int64(3200)
		historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start.Format(time.DateOnly), authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
		historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: today.Format(time.DateOnly)}))
		if i < 27 {
			historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease.ID, authorization.LeaseNoticeInput{Action: "create", Category: "customer_early", Initiator: "customer", NoticeOn: start.Format(time.DateOnly), EffectiveOn: start.AddDate(0, 0, 1).Format(time.DateOnly), Reason: "Historical agreement ended", OperationKey: newOperationKey()}))
		}
		if i == 26 {
			archivedID = lease.ID
			historyCheck(t, svc.LeaseLifecycle(ctx, actor, lease.ID, false))
		}
	}
	path := fmt.Sprintf("/assets/%d", a.ID)
	body := requestBody(t, web, cookie, "GET", path, 200)
	assertContains(t, body, "Current Lease coverage")
	assertContains(t, body, "Newest current coverage")
	assertContains(t, body, "Historical coverage 00")
	assertNotContains(t, body, "Historical coverage 26")
	page2 := requestBody(t, web, cookie, "GET", path+"?coverage_page=2", 200)
	assertContains(t, page2, "Historical coverage 26")
	assertContains(t, page2, "Coverage: "+today.AddDate(0, 0, -22).Format(time.DateOnly)+" through "+today.AddDate(0, 0, -21).Format(time.DateOnly))
	assertNotContains(t, page2, "Historical coverage 00")
	historySection := func(body string) string {
		_, section, _ := strings.Cut(body, `<ul aria-label="Lease relationship history">`)
		section, _, _ = strings.Cut(section, `</ul>`)
		return section
	}
	historyEqual(t, "stable second page", historySection(requestBody(t, web, cookie, "GET", path+"?coverage_page=2", 200)), historySection(page2))
	reader := client.User.Create().SetCompanyID(company).SetName("Coverage reader").SetEmail("coverage-reader@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	grants := []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "asset.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, actor, 0, "Coverage reader", grants)
	historyCheck(t, err)
	historyCheck(t, svc.AssignUser(ctx, actor, reader.ID, role))
	readerCookie := sessionCookie(t, ctx, sessions, reader.ID)
	body = requestBody(t, web, readerCookie, "GET", path+"?coverage_page=2", 200)
	assertContains(t, body, "Historical coverage 26")
	assertNotContains(t, body, fmt.Sprintf(`href="/leases/%d"`, archivedID))
	assertNotContains(t, body, "Coverage: "+today.AddDate(0, 0, -22).Format(time.DateOnly))
	assertNotContains(t, body, fmt.Sprintf(`href="/leases/%d/billing"`, archivedID))
	requestBody(t, web, readerCookie, "GET", fmt.Sprintf("/leases/%d", archivedID), 404)
	t.Run("persisted coverage pagination", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("BROWSER_TESTS=1 required")
		}
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-workspace-coverage.cjs", server.URL, cookie.Value, readerCookie.Value, path, fmt.Sprint(archivedID)).CombinedOutput()
		t.Logf("coverage browser: %s", output)
		historyCheck(t, err)
	})
	_, err = svc.SaveRole(ctx, actor, role, "Coverage reader", grants[1:], 1)
	historyCheck(t, err)
	assertNotContains(t, requestBody(t, web, readerCookie, "GET", path+"?coverage_page=2", 200), "Historical coverage")
	_, err = svc.SaveRole(ctx, actor, role, "Coverage reader", grants[:1], 2)
	historyCheck(t, err)
	requestBody(t, web, readerCookie, "GET", path+"?coverage_page=2", 403)
}
