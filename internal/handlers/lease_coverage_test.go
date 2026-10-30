package handlers

import (
	"context"
	"errors"
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

func TestLeaseCoverageHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Coverage customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Coverage equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Outgoing").SaveX(ctx)
	b := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Incoming").SaveX(ctx)
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	start := today.AddDate(0, -2, 0).Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: start, EndsOn: today.AddDate(1, 0, 0).Format(time.DateOnly), PremisesID: &l.ID, AssetIDs: []int64{a.ID}}))
	zero, rent := int64(0), int64(3200)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start}))
	var originalInvoices string
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(i) ORDER BY id)::text FROM invoices i`).Scan(&originalInvoices))
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/leases/%d/coverage", lease.ID)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Coverage changes")
	values := url.Values{"action": {"create"}, "kind": {"replace"}, "effective_on": {today.Format(time.DateOnly)}, "outgoing_id": {fmt.Sprint(a.ID)}, "incoming_id": {fmt.Sprint(b.ID)}, "reason": {"Equipment replacement"}, "operation_key": {"coverage-replace-0001"}, "expected_revision": {"0"}, "revision": {"0"}, "event_id": {"0"}}
	fieldPost(t, web, cookie, path, values, 303)
	fieldPost(t, web, cookie, path, values, 303)
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_coverage_events WHERE lease_id=$1`, lease.ID).Scan(&count))
	historyEqual(t, "replay event count", count, 1)
	var outgoingEnd, incomingStart string
	historyCheck(t, pool.QueryRow(ctx, `SELECT ends_on::text FROM lease_asset_dependencies WHERE lease_id=$1 AND asset_id=$2`, lease.ID, a.ID).Scan(&outgoingEnd))
	historyCheck(t, pool.QueryRow(ctx, `SELECT starts_on::text FROM lease_asset_dependencies WHERE lease_id=$1 AND asset_id=$2`, lease.ID, b.ID).Scan(&incomingStart))
	historyEqual(t, "inclusive outgoing end", outgoingEnd, today.AddDate(0, 0, -1).Format(time.DateOnly))
	historyEqual(t, "incoming start", incomingStart, today.Format(time.DateOnly))
	var revision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT max(id) FROM lease_coverage_revisions`).Scan(&revision))
	fieldPost(t, web, cookie, path, url.Values{"action": {"create"}, "kind": {"remove"}, "effective_on": {today.AddDate(0, 0, 2).Format(time.DateOnly)}, "outgoing_id": {fmt.Sprint(b.ID)}, "reason": {"Invalid last removal"}, "operation_key": {"coverage-last-remove"}, "expected_revision": {fmt.Sprint(revision)}}, 409)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_coverage_events WHERE lease_id=$1`, lease.ID).Scan(&count))
	historyEqual(t, "failed change has no event", count, 1)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var flags int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_review_flags`).Scan(&flags))
	if flags == 0 {
		t.Fatal("generated billing was not flagged")
	}
	var afterInvoices string
	historyCheck(t, pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(i) ORDER BY id)::text FROM invoices i`).Scan(&afterInvoices))
	historyEqual(t, "generated Invoices are immutable snapshots", afterInvoices, originalInvoices)
	t.Run("commit-time grants and failure rollback", func(t *testing.T) {
		var grants []authorization.Grant
		for _, key := range []string{"lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage", "asset.view"} {
			grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
		}
		role, err := svc.SaveRole(ctx, actor, 0, "Coverage writer", grants)
		historyCheck(t, err)
		u := client.User.Create().SetCompanyID(company).SetName("Coverage writer").SetEmail("coverage68@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, actor, u.ID, role))
		writerCookie := sessionCookie(t, ctx, sessions, u.ID)
		tables := []string{"lease_coverage_events", "lease_coverage_revisions", "lease_asset_dependencies", "lease_master_dependencies", "lease_billing_source_changes", "lease_invoice_review_flags", "lease_billing_obligations", "lease_invoice_generations", "invoices", "assets", "asset_association_periods", "authorization_audit"}
		before := historyDurableRows(t, pool, tables)
		valid := url.Values{"action": {"create"}, "kind": {"add"}, "effective_on": {today.AddDate(0, 0, 5).Format(time.DateOnly)}, "incoming_id": {fmt.Sprint(a.ID)}, "reason": {"More equipment"}, "operation_key": {"coverage-grant-check"}, "expected_revision": {fmt.Sprint(revision)}, "rent_cents": {"1200"}, "tax_rate": {"0"}}
		for _, key := range []string{"lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage", "asset.view"} {
			status := 403
			if key == "asset.view" {
				status = 404
			}
			queuedChildPost(t, pool, web, writerCookie, path, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, []any{role, key}, status, valid)
			_, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,'A')`, role, key)
			historyCheck(t, err)
		}
		_, err = pool.Exec(ctx, `CREATE FUNCTION reject_coverage_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'lease.coverage.%' THEN RAISE EXCEPTION 'injected coverage audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_coverage_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_coverage_audit()`)
		historyCheck(t, err)
		fieldPost(t, web, writerCookie, path, valid, 409)
		_, err = pool.Exec(ctx, `DROP TRIGGER reject_coverage_audit ON authorization_audit`)
		historyCheck(t, err)
		historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_coverage_events WHERE lease_id=$1`, lease.ID).Scan(&count))
		historyEqual(t, "denial/failure retains accepted event only", count, 1)
		historyEqual(t, "denial/failure is all-or-zero across coverage, billing, placement and immutable history", historyDurableRows(t, pool, tables), before)
		_, err = svc.ChangeLeaseCoverage(ctx, authorization.Actor{UserID: admin, Channel: "api"}, lease.ID, authorization.LeaseCoverageInput{})
		if !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("field API coverage write: %v", err)
		}
	})
	t.Run("browser journeys", func(t *testing.T) {
		if os.Getenv("BROWSER_TESTS") != "1" {
			t.Skip("BROWSER_TESTS=1 required")
		}
		mux := http.NewServeMux()
		mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
		mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
		server := httptest.NewServer(mux)
		defer server.Close()
		browserCtx, cancel := context.WithTimeout(ctx, 150*time.Second)
		defer cancel()
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-coverage.cjs", server.URL, cookie.Value, fmt.Sprint(lease.ID), fmt.Sprint(a.ID), fmt.Sprint(b.ID), today.Format(time.DateOnly)).CombinedOutput()
		t.Logf("coverage browser: %s", output)
		historyCheck(t, err)
	})
	t.Run("concurrent replacement and archive", func(t *testing.T) {
		review, err := svc.ReviewLeaseCoverage(ctx, actor, lease.ID)
		historyCheck(t, err)
		x := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Contender one").SaveX(ctx)
		y := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Contender two").SaveX(ctx)
		results := make(chan error, 2)
		for _, asset := range []int64{x.ID, y.ID} {
			go func(asset int64) {
				_, err := svc.ChangeLeaseCoverage(ctx, actor, lease.ID, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "replace", EffectiveOn: today.AddDate(0, 0, 1).Format(time.DateOnly), OutgoingID: b.ID, IncomingID: asset}, Action: "create", Reason: "Concurrent equipment replacement", OperationKey: fmt.Sprintf("concurrent-coverage-%d", asset), ExpectedRevision: review.Revision})
				results <- err
			}(asset)
		}
		passed, conflicted := 0, 0
		for range 2 {
			err := <-results
			if err == nil {
				passed++
			} else if errors.Is(err, authorization.ErrConflict) {
				conflicted++
			} else {
				t.Fatal(err)
			}
		}
		historyEqual(t, "one replacement commits", passed, 1)
		historyEqual(t, "other replacement is stale", conflicted, 1)
		var covered int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1 AND asset_id=ANY($2::bigint[])`, lease.ID, []int64{x.ID, y.ID}).Scan(&covered))
		if err := svc.AssetLifecycle(ctx, actor, covered, false); err == nil {
			t.Fatal("future coverage allowed Asset archive")
		}
		if err := svc.CustomerLifecycle(ctx, actor, c.ID, false); err == nil {
			t.Fatal("in-force Lease allowed Customer archive")
		}
		if err := svc.CustomerChildLifecycle(ctx, actor, c.ID, l.ID, "location", false); err == nil {
			t.Fatal("in-force Lease allowed premises archive")
		}
	})
}
