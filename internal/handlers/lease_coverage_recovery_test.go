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

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestLeaseCoverageRecoveryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Recovery customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Outgoing").SaveX(ctx)
	b := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Incoming").SaveX(ctx)
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	date := today.Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: date, EndsOn: today.AddDate(1, 0, 0).Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
	zero, rent := int64(0), int64(3200)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, date, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: date}))
	grants := []authorization.Grant{}
	for _, key := range []string{"lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage", "asset.view"} {
		grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, actor, 0, "Coverage recovery", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Recovery writer").SetEmail("coverage-recovery@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, actor, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/leases/%d/coverage", lease.ID)
	tables := []string{"lease_coverage_events", "lease_coverage_revisions", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_source_changes", "lease_invoice_review_flags", "lease_billing_obligations", "lease_invoice_generations", "invoices", "assets", "asset_association_periods", "authorization_audit"}
	snapshot := func() map[string]string { return leasePreservationRows(t, pool, tables) }
	before := snapshot()
	values := url.Values{"action": {"create"}, "kind": {"remove"}, "effective_on": {date}, "outgoing_id": {fmt.Sprint(a.ID)}, "incoming_id": {"0"}, "reason": {"Retained private proposal"}, "operation_key": {"coverage-recovery-identity"}, "expected_revision": {"0"}, "event_id": {"0"}, "revision": {"0"}}
	body := fieldPost(t, web, cookie, path, values, 409).Body.String()
	for _, text := range []string{"At least one Asset", "Replace", "Retained private proposal", "data-error-summary", "#coverage-effective_on"} {
		assertContains(t, body, text)
	}
	historyEqual(t, "last Asset rejection is atomic", snapshot(), before)
	values.Set("kind", "replace")
	values.Set("incoming_id", fmt.Sprint(b.ID))
	values.Set("effective_on", "bad-date")
	body = fieldPost(t, web, cookie, path, values, 409).Body.String()
	assertContains(t, body, "bad-date")
	historyEqual(t, "bad date is atomic", snapshot(), before)
	values.Set("kind", "add")
	values.Set("effective_on", date)
	values.Set("rent_cents", "12.5")
	body = fieldPost(t, web, cookie, path, values, 409).Body.String()
	assertContains(t, body, "exact nonnegative whole cents")
	assertContains(t, body, `value="12.5"`)
	historyEqual(t, "bad amount is atomic", snapshot(), before)
	for _, key := range []string{"lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage", "asset.view"} {
		_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, key)
		historyCheck(t, err)
		status := 403
		if key == "lease.view" || key == "asset.view" {
			status = 404
		}
		body = fieldPost(t, web, cookie, path, values, status).Body.String()
		if strings.Contains(body, "Retained private proposal") || strings.Contains(body, "12.5") || strings.Contains(body, "coverage-recovery") {
			t.Fatalf("access loss disclosed proposal: %s", body)
		}
		_, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,'A')`, role, key)
		historyCheck(t, err)
	}
	historyEqual(t, "access loss never writes coverage", snapshot(), before)
	t.Run("browser rejection correction stale and access loss", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("BROWSER_TESTS=1 required")
		}
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		// Test-only observation and revocation controls; all product requests use
		// the production router, session, CSRF, authorization and database services.
		mux.HandleFunc("/test-snapshot", func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(snapshot()) })
		mux.HandleFunc("/test-revoke", func(w http.ResponseWriter, r *http.Request) {
			_, e := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.billing.view'`, role)
			if e != nil {
				http.Error(w, e.Error(), 500)
			}
		})
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
		defer cancel()
		output, e := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-coverage-recovery.cjs", server.URL, cookie.Value, fmt.Sprint(lease.ID), fmt.Sprint(a.ID), fmt.Sprint(b.ID), date).CombinedOutput()
		t.Logf("coverage recovery browser: %s", output)
		historyCheck(t, e)
		var events, revisions int
		historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM lease_coverage_events),(SELECT count(*) FROM lease_coverage_revisions)`).Scan(&events, &revisions))
		historyEqual(t, "one corrected replacement event", events, 1)
		historyEqual(t, "replacement plus two explicit revisions only", revisions, 3)
	})
}
