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

func TestLeaseWorkspaceTerminatedArchiveBannerHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Historical Lease customer").SaveX(ctx)
	premises := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Historical premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Workspace equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Retained equipment").SaveX(ctx)
	lease, err := svc.RegisterDraftLease(ctx, actor, customer.ID)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	start := today.AddDate(1, 0, 0).Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: start, EndsOn: today.AddDate(2, 0, 0).Format(time.DateOnly), PremisesID: &premises.ID, AssetIDs: []int64{asset.ID}}))
	zero, rent := int64(0), int64(3200)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: asset.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start}))
	path := fmt.Sprintf("/leases/%d", lease.ID)
	assertNotContains(t, requestBody(t, web, cookie, "GET", path, 200), "Archive Lease")
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease.ID, authorization.LeaseNoticeInput{Action: "create", Category: "cancel_before_start", Initiator: "mutual", NoticeOn: today.Format(time.DateOnly), Reason: "Agreement cancelled before commencement", OperationKey: "workspace-cancel-before-start-0001"}))
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Archive Lease")
	fieldPost(t, web, cookie, path+"/delete", url.Values{}, 303)
	preview, err := svc.PreviewCustomerArchive(ctx, actor, customer.ID)
	historyCheck(t, err)
	fieldPost(t, web, cookie, fmt.Sprintf("/customers/%d/delete", customer.ID), url.Values{"archive_snapshot": {preview.Snapshot}}, 303)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Restore Lease")
	reader := client.User.Create().SetCompanyID(company).SetName("Historical reader").SetEmail("workspace-history@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	grants := []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.archived.view", Scope: authorization.All}}
	role, err := svc.SaveRole(ctx, actor, 0, "Historical read only", grants)
	historyCheck(t, err)
	historyCheck(t, svc.AssignUser(ctx, actor, reader.ID, role))
	readerCookie := sessionCookie(t, ctx, sessions, reader.ID)
	assertNotContains(t, requestBody(t, web, readerCookie, "GET", path, 200), "Restore Lease")
	fieldPost(t, web, readerCookie, path+"/restore", url.Values{}, 403)
	grants = append(grants, authorization.Grant{Permission: "lease.restore", Scope: authorization.All})
	_, err = svc.SaveRole(ctx, actor, role, "Historical restore only", grants, 1)
	historyCheck(t, err)
	assertContains(t, requestBody(t, web, readerCookie, "GET", path, 200), "Restore Lease")
	before := historyDurableRows(t, pool, []string{"customers", "locations", "assets", "lease_notice_revisions", "lease_finalizations"})
	t.Run("responsive persisted restore", func(t *testing.T) {
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
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-workspace-archive.cjs", server.URL, readerCookie.Value, fmt.Sprint(lease.ID)).CombinedOutput()
		t.Logf("historical restore browser: %s", output)
		historyCheck(t, err)
	})
	fieldPost(t, web, readerCookie, path+"/restore", url.Values{}, 303)
	fieldPost(t, web, readerCookie, path+"/restore", url.Values{}, 303)
	historyEqual(t, "historical restore never revives dependencies", historyDurableRows(t, pool, []string{"customers", "locations", "assets", "lease_notice_revisions", "lease_finalizations"}), before)
	var noEffects bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=$1) AND NOT EXISTS(SELECT 1 FROM invoices WHERE lease_id=$1)`, lease.ID).Scan(&noEffects))
	historyEqual(t, "cancelled coverage and billing remain absent", noEffects, true)
	var events int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='lease' AND target_id=$1 AND action='lease.restore'`, lease.ID).Scan(&events))
	historyEqual(t, "replayed restore has one audit", events, 1)
}
