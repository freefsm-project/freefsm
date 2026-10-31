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

func TestLeaseBillingRevisionPersistence(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Revision customer").SaveX(ctx)
	location := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Revision premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Revision equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Revised equipment").SaveX(ctx)
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	var today time.Time
	historyCheck(t, pool.QueryRow(ctx, `SELECT lease_dependency_today($1)`, company).Scan(&today))
	start := today.AddDate(0, -2, 0).Format(time.DateOnly)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: start, EndsOn: today.AddDate(1, 0, 0).Format(time.DateOnly), PremisesID: &location.ID, AssetIDs: []int64{asset.ID}}))
	zero, rent := int64(0), int64(3200)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, start, authorization.DraftLeaseTerms{AssetID: asset.ID, InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: start}))
	original := historyDurableRows(t, pool, []string{"invoices", "lease_finalizations", "lease_invoice_generations"})
	path := fmt.Sprintf("/leases/%d/billing/revisions", lease.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Revise billing")
	values := url.Values{"expected_revision": {"0"}, "effective_on": {start}, "asset_id": {fmt.Sprint(asset.ID)}, fmt.Sprintf("rent_cents_%d", asset.ID): {"4500"}, fmt.Sprintf("tax_rate_%d", asset.ID): {"5"}, "reason": {"Agreed prospective rate"}, "operation_key": {"billing-revision-persistence-0001"}}
	fieldPost(t, web, cookie, path, values, 303)
	fieldPost(t, web, cookie, path, values, 303)
	historyEqual(t, "generated facts retained", historyDurableRows(t, pool, []string{"invoices", "lease_finalizations", "lease_invoice_generations"}), original)
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_revisions WHERE lease_id=$1`, lease.ID).Scan(&count))
	historyEqual(t, "one revision after replay", count, 1)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Agreed prospective rate")
	assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d/billing/history", lease.ID), 200), "Agreed prospective rate")
	t.Run("independent authority API and queued grant loss", func(t *testing.T) {
		role, err := svc.SaveRole(ctx, actor, 0, "Base only revision denied", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}})
		historyCheck(t, err)
		user := client.User.Create().SetCompanyID(company).SetName("Base only").SetEmail("revision71@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
		denied := sessionCookie(t, ctx, sessions, user.ID)
		body := requestBody(t, web, denied, "GET", path, 404)
		assertNotContains(t, body, "Agreed prospective rate")
		assertNotContains(t, requestBody(t, web, denied, "GET", fmt.Sprintf("/leases/%d/history", lease.ID), 200), "Agreed prospective rate")
		fieldPost(t, web, denied, path, values, 403)
		api := actor
		api.Channel = "api"
		if _, err = svc.ReviewLeaseBillingRevisions(ctx, api, lease.ID); err == nil {
			t.Fatal("API read exposed terms")
		}
		if err = svc.ReviseLeaseBilling(ctx, api, lease.ID, authorization.LeaseBillingRevisionInput{}); err == nil {
			t.Fatal("API accepted revision")
		}
		role, err = svc.SaveRole(ctx, actor, 0, "Billing only writer", []authorization.Grant{{Permission: "lease.billing.view", Scope: authorization.All}, {Permission: "lease.billing.manage", Scope: authorization.All}})
		historyCheck(t, err)
		historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
		queuedChildPost(t, pool, web, denied, path, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='lease.billing.manage'`, []any{role}, 403, values)
		_, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'lease.billing.manage','A')`, role)
		historyCheck(t, err)
		review, err := svc.ReviewLeaseBillingRevisions(ctx, actor, lease.ID)
		historyCheck(t, err)
		proposal := authorization.LeaseBillingRevisionInput{ExpectedRevision: review.Revision, EffectiveOn: start, Reason: "Independent billing only", OperationKey: "billing-only-revision-0001", Changes: []authorization.LeaseBillingChange{{AssetID: asset.ID, RentCents: 4500, TaxRate: "5"}}}
		historyCheck(t, svc.ReviseLeaseBilling(ctx, authorization.Actor{UserID: user.ID}, lease.ID, proposal))
	})
	t.Run("atomic rollback retained validation immutable history", func(t *testing.T) {
		review, err := svc.ReviewLeaseBillingRevisions(ctx, actor, lease.ID)
		historyCheck(t, err)
		values.Set("expected_revision", fmt.Sprint(review.Revision))
		values.Set("operation_key", "billing-rollback-revision-0001")
		values.Set("reason", "Retained failed billing proposal")
		tables := []string{"lease_billing_revisions", "lease_billing_source_changes", "lease_invoice_review_flags", "invoices", "authorization_audit"}
		before := historyDurableRows(t, pool, tables)
		_, err = pool.Exec(ctx, `CREATE FUNCTION reject_billing_revision_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.billing.revise' THEN RAISE EXCEPTION 'private injected failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_billing_revision_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_billing_revision_audit()`)
		historyCheck(t, err)
		response := fieldPost(t, web, cookie, path, values, 409)
		assertContains(t, response.Body.String(), "transaction was rolled back")
		assertContains(t, response.Body.String(), "Retained failed billing proposal")
		assertNotContains(t, response.Body.String(), "private injected failure")
		historyEqual(t, "all or zero", historyDurableRows(t, pool, tables), before)
		_, err = pool.Exec(ctx, `DROP TRIGGER reject_billing_revision_audit ON authorization_audit`)
		historyCheck(t, err)
		fieldPost(t, web, cookie, path, values, 303)
		review, err = svc.ReviewLeaseBillingRevisions(ctx, actor, lease.ID)
		historyCheck(t, err)
		invalid := url.Values{"expected_revision": {fmt.Sprint(review.Revision)}, "effective_on": {start}, "asset_id": {"99999999"}, "rent_cents_99999999": {"123456"}, "tax_rate_99999999": {"5"}, "reason": {"Retain unavailable submitted selection"}, "operation_key": {"billing-unavailable-selection-0001"}}
		body := fieldPost(t, web, cookie, path, invalid, 409).Body.String()
		assertContains(t, body, "123456")
		assertContains(t, body, "unavailable in the current billing selection")
		for _, q := range []string{`UPDATE lease_billing_revisions SET reason='rewrite'`, `DELETE FROM lease_billing_revisions`} {
			if _, err = pool.Exec(ctx, q); err == nil {
				t.Fatal("billing history mutable")
			}
		}
	})
	t.Run("competing revisions one winner", func(t *testing.T) {
		review, err := svc.ReviewLeaseBillingRevisions(ctx, actor, lease.ID)
		historyCheck(t, err)
		results := make(chan error, 2)
		for i := range 2 {
			go func(i int) {
				results <- svc.ReviseLeaseBilling(ctx, actor, lease.ID, authorization.LeaseBillingRevisionInput{ExpectedRevision: review.Revision, EffectiveOn: start, Reason: "Concurrent proposal", OperationKey: fmt.Sprintf("billing-race-revision-%04d", i), Changes: []authorization.LeaseBillingChange{{AssetID: asset.ID, RentCents: int64(4600 + i), TaxRate: "5"}}})
			}(i)
		}
		successes := 0
		for range 2 {
			if <-results == nil {
				successes++
			}
		}
		historyEqual(t, "one accepted stale competitor", successes, 1)
	})
	t.Run("persisted browser navigation validation compare revise discard", func(t *testing.T) {
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
		output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-billing-revision.cjs", server.URL, cookie.Value, fmt.Sprint(lease.ID), fmt.Sprint(asset.ID), start).CombinedOutput()
		t.Logf("billing revision browser: %s", output)
		historyCheck(t, err)
	})
}

func TestLeaseBillingRevisionProspectiveGeneration(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	clock := func(date string) {
		t.Helper()
		_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+date+`'::date $$`)
		historyCheck(t, err)
	}
	clock("2024-01-01")
	id, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	var asset, typ int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT a.id,a.asset_type_id FROM assets a JOIN lease_asset_dependencies d ON d.asset_id=a.id WHERE d.lease_id=$1`, id).Scan(&asset, &typ))
	incoming := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Revised rate replacement").SaveX(ctx)
	in := authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-02-15", Reason: "Future agreed increase", OperationKey: "prospective-billing-rate-0001", Changes: []authorization.LeaseBillingChange{{AssetID: asset, RentCents: 4500, TaxRate: "5"}}}
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, id, in))
	_, err := svc.ChangeLeaseCoverage(ctx, actor, id, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "replace", EffectiveOn: "2024-02-20", OutgoingID: asset, IncomingID: incoming.ID}, Action: "create", Reason: "Carry approved revised rent", OperationKey: "prospective-billing-replace-0001"})
	historyCheck(t, err)
	clock("2024-02-01")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var total, rate string
	historyCheck(t, pool.QueryRow(ctx, `SELECT i.financial::jsonb->'result'->>'total_cents',i.line_items->0->>'unit_price_decimal' FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on='2024-03-01'`, id).Scan(&total, &rate))
	historyEqual(t, "replacement inherits revised full monthly rent", rate, "45.00")
	historyEqual(t, "ordinary calculator tax", total, "4725")
	before := historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations"})
	review, err := svc.ReviewLeaseBillingRevisions(ctx, actor, id)
	historyCheck(t, err)
	in.ExpectedRevision = review.Revision
	in.OperationKey = "prospective-billing-rate-0002"
	in.EffectiveOn = "2024-03-01"
	in.Changes = []authorization.LeaseBillingChange{{AssetID: incoming.ID, RentCents: 6000, TaxRate: "5"}}
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, id, in))
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyEqual(t, "generated March unchanged", historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations"}), before)
	clock("2024-03-02")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyCheck(t, pool.QueryRow(ctx, `SELECT i.financial::jsonb->'result'->>'total_cents' FROM invoices i JOIN lease_invoice_generations g ON g.invoice_id=i.id JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1 AND o.kind='monthly' AND o.due_on='2024-04-01'`, id).Scan(&total))
	historyEqual(t, "next ungenerated month revised", total, "6300")
}
