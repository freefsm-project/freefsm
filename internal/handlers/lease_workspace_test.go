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
)

func TestLeaseWorkspaceRelationshipSearchHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Workspace customer zebra").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Workspace premises orchard").SetAddress1("829 Walnut Avenue").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Workspace equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Workspace asset").SaveX(ctx)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	var number string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id,number_label FROM leases`).Scan(&id, &number))
	fieldPost(t, web, cookie, fmt.Sprintf("/leases/%d", id), url.Values{"revision": {"0"}, "title": {"Workspace agreement"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	var assetNumber string
	historyCheck(t, pool.QueryRow(ctx, `SELECT number FROM assets WHERE id=$1`, a.ID).Scan(&assetNumber))
	for _, search := range []string{number, "agreement", "ZEBRA", "orchard", "Walnut", assetNumber} {
		t.Run(search, func(t *testing.T) {
			body := requestBody(t, web, cookie, "GET", "/leases?search="+url.QueryEscape(search), 200)
			assertContains(t, body, "Workspace agreement")
			assertContains(t, body, "1 visible Leases")
		})
	}
	body := requestBody(t, web, cookie, "GET", "/leases?lifecycle=active", 200)
	if strings.Contains(body, "Workspace agreement") {
		t.Fatal("Draft matched Active filter")
	}
	assertContains(t, body, "0 visible Leases")
	requestBody(t, web, cookie, "GET", "/leases?customer=not-an-id", 400)
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	start := today.AddDate(0, 0, 5).Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, id, authorization.DraftLeaseDetailsInput{Revision: 1, Title: "Workspace agreement", StartsOn: start, EndsOn: today.AddDate(0, 0, 20).Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
	zero, rent := int64(0), int64(3200)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, id, 0, start, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, id, authorization.LeaseFinalizationInput{DetailsRevision: 2, BillingRevision: 1, ManagedBillingStart: start}))
	for _, route := range []string{fmt.Sprintf("/customers/%d", c.ID), fmt.Sprintf("/assets/%d", a.ID), fmt.Sprintf("/locations/%d", l.ID)} {
		assertContains(t, requestBody(t, web, cookie, "GET", route, 200), "Workspace agreement")
	}
	for _, queue := range []string{"starts", "renewals", "placement", "monthly"} {
		assertContains(t, requestBody(t, web, cookie, "GET", "/leases?queue="+queue, 200), "Workspace agreement")
	}
	for _, queue := range []string{"termination", "failed", "review"} {
		body := requestBody(t, web, cookie, "GET", "/leases?queue="+queue, 200)
		assertNotContains(t, body, "Workspace agreement")
	}
	// Persisted worker failure fixtures exercise the discovery boundary without
	// depending on a background worker's timing. Detailed failure text remains on
	// the independently authorized Billing surface.
	_, err := pool.Exec(ctx, `INSERT INTO lease_billing_period_failures(lease_id,kind,due_on,failure) VALUES($1,'monthly',$2::date,'private worker failure')`, id, today.AddDate(0, 2, 0).Format(time.DateOnly))
	historyCheck(t, err)
	body = requestBody(t, web, cookie, "GET", "/leases?queue=failed", 200)
	assertContains(t, body, "Workspace agreement")
	assertNotContains(t, body, "private worker failure")
	role, err := svc.SaveRole(ctx, actor, 0, "Workspace base only", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Workspace reader").SetEmail("workspace73@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, actor, u.ID, role))
	reader := sessionCookie(t, ctx, sessions, u.ID)
	body = requestBody(t, web, reader, "GET", "/leases?queue=starts", 200)
	assertContains(t, body, "Workspace agreement")
	assertNotContains(t, body, "Monthly billing")
	for _, queue := range []string{"monthly", "failed", "review"} {
		requestBody(t, web, reader, "GET", "/leases?queue="+queue, 403)
	}
	for range 26 {
		_, err := svc.RegisterDraftLease(ctx, actor, c.ID)
		historyCheck(t, err)
	}
	for _, size := range []int{25, 50, 100} {
		body := requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases?customer=%d&per_page=%d", c.ID, size), 200)
		assertContains(t, body, "27 visible Leases")
		if size == 25 {
			assertContains(t, body, ">Next</a>")
		} else {
			assertNotContains(t, body, ">Next</a>")
		}
	}
	t.Run("persisted browser filters", func(t *testing.T) {
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
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-workspace.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID), fmt.Sprint(l.ID)).CombinedOutput()
		t.Logf("workspace browser: %s", output)
		historyCheck(t, err)
	})
	// Cancelling and archiving the scheduled agreement keeps only a bounded
	// reference on a visible master when archived Lease access is absent.
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, id, authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "company", NoticeOn: today.Format(time.DateOnly), EffectiveOn: today.Format(time.DateOnly), Reason: "Cancelled before commencement", OperationKey: newOperationKey()}))
	assertContains(t, requestBody(t, web, cookie, "GET", "/leases?queue=review", 200), "Workspace agreement")
	fieldPost(t, web, cookie, fmt.Sprintf("/leases/%d/delete", id), nil, 303)
	grants := []authorization.Grant{}
	for _, permission := range []string{"lease.view", "customer.view", "asset.view", "location.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	_, err = svc.SaveRole(ctx, actor, role, "Workspace base only", grants, 1)
	historyCheck(t, err)
	for _, route := range []string{fmt.Sprintf("/customers/%d", c.ID), fmt.Sprintf("/locations/%d", l.ID)} {
		body := requestBody(t, web, reader, "GET", route, 200)
		assertContains(t, body, "Workspace agreement")
		assertContains(t, body, "Archived")
		assertNotContains(t, body, fmt.Sprintf(`href="/leases/%d"`, id))
		assertNotContains(t, body, fmt.Sprintf(`href="/leases/%d/billing"`, id))
	}
	requestBody(t, web, reader, "GET", fmt.Sprintf("/leases/%d", id), 404)
	assertNotContains(t, requestBody(t, web, reader, "GET", "/leases?search=agreement", 200), "Workspace agreement")
	active, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	activeStart := today.AddDate(0, 0, -5).Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, active.ID, authorization.DraftLeaseDetailsInput{Title: "Pending notice workspace", StartsOn: activeStart, EndsOn: today.AddDate(1, 0, 0).Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, active.ID, 0, activeStart, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, active.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: today.Format(time.DateOnly)}))
	ending := today.AddDate(0, 0, 30).Format(time.DateOnly)
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, active.ID, authorization.LeaseNoticeInput{Action: "create", Category: "customer_early", Initiator: "customer", NoticeOn: today.Format(time.DateOnly), EffectiveOn: ending, Reason: "Negotiated end", OperationKey: newOperationKey()}))
	body = requestBody(t, web, cookie, "GET", "/leases?queue=termination", 200)
	assertContains(t, body, "Pending notice workspace")
	assertContains(t, body, "Notice ending date: "+ending)
	assertNotContains(t, requestBody(t, web, cookie, "GET", "/leases?ending_through="+today.Format(time.DateOnly), 200), "Pending notice workspace")
}
