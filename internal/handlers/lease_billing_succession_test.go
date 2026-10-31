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

func TestLeaseBillingRevisionReAddChargeSuccession(t *testing.T) {
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
	original := historyDurableRows(t, pool, []string{"invoices", "lease_finalizations", "lease_invoice_generations"})
	var asset, typ int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT a.id,a.asset_type_id FROM assets a JOIN lease_asset_dependencies d ON d.asset_id=a.id WHERE d.lease_id=$1`, id).Scan(&asset, &typ))
	other := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Continuous zero tax allocation").SaveX(ctx)
	change := func(in authorization.LeaseCoverageInput) {
		t.Helper()
		review, err := svc.ReviewLeaseCoverage(ctx, actor, id)
		historyCheck(t, err)
		in.ExpectedRevision = review.Revision
		in.Action, in.Reason = "create", "Explicit allocation succession"
		in.OperationKey = fmt.Sprintf("succession-coverage-%d-%s", in.IncomingID+in.OutgoingID, in.EffectiveOn)
		_, err = svc.ChangeLeaseCoverage(ctx, actor, id, in)
		historyCheck(t, err)
	}
	rent, charge := int64(5000), int64(10000)
	change(authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", IncomingID: other.ID, EffectiveOn: "2024-01-31"}, RentCents: &rent, TaxRate: "0"})
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, id, authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-02-01", Reason: "Old allocation tax", OperationKey: "succession-old-tax-0001", Changes: []authorization.LeaseBillingChange{{AssetID: asset, RentCents: 5000, TaxRate: "7"}}}))
	change(authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "remove", OutgoingID: asset, EffectiveOn: "2024-03-10"}})
	change(authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", IncomingID: asset, EffectiveOn: "2024-06-10"}, RentCents: &rent, ChargeCents: &charge, TaxRate: "5"})
	grouped := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Grouped zero tax charge").SaveX(ctx)
	groupRent, groupCharge := int64(1000), int64(2000)
	change(authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", IncomingID: grouped.ID, EffectiveOn: "2024-06-10"}, RentCents: &groupRent, ChargeCents: &groupCharge, TaxRate: "0"})
	historyEqual(t, "prior generated snapshots unchanged by succession", historyDurableRows(t, pool, []string{"invoices", "lease_finalizations", "lease_invoice_generations"}), original)
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_succession_charge() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.line_items->0->>'charge_kind'='coverage' THEN RAISE EXCEPTION 'injected charge generation failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_succession_charge BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION reject_succession_charge()`)
	historyCheck(t, err)
	clock("2024-06-01")
	if err = svc.GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("expected isolated coverage generation failure")
	}
	var obligation int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='coverage' AND due_on='2024-06-10'`, id).Scan(&obligation))
	preview, err := svc.ReviewLeaseBillingPeriod(ctx, actor, id, obligation)
	historyCheck(t, err)
	historyEqual(t, "pending preview after rollback", preview.InvoiceID, int64(0))
	historyEqual(t, "grouped Add charge uses explicit 5 percent plus zero tax line", preview.TotalCents, int64(12500))
	historyEqual(t, "one grouped obligation with both Assets", len(preview.Lines), 2)
	historyEqual(t, "explicit Add amount retained", preview.Lines[0].Amount, "100.00")
	historyEqual(t, "new Add does not cite old allocation revision", preview.Lines[0].BillingRevisionID, int64(0))
	_, err = pool.Exec(ctx, `DROP TRIGGER reject_succession_charge ON invoices`)
	historyCheck(t, err)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	generated, err := svc.ReviewLeaseBillingPeriod(ctx, actor, id, obligation)
	historyCheck(t, err)
	historyEqual(t, "generated grouped Add amount and tax", generated.TotalCents, int64(12500))
	historyEqual(t, "generated Add provenance", generated.Lines[0].BillingRevisionID, int64(0))
	before := historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations"})
	billing, err := svc.ReviewLeaseBillingRevisions(ctx, actor, id)
	historyCheck(t, err)
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, id, authorization.LeaseBillingRevisionInput{ExpectedRevision: billing.Revision, EffectiveOn: "2024-07-01", Reason: "Subsequent revision of new allocation", OperationKey: "succession-new-allocation-0001", Changes: []authorization.LeaseBillingChange{{AssetID: asset, RentCents: 6000, TaxRate: "6"}}}))
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyEqual(t, "later revision preserves all generated snapshots and replay", historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations"}), before)
	var monthly int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly' AND due_on='2024-07-01'`, id).Scan(&monthly))
	july, err := svc.ReviewLeaseBillingPeriod(ctx, actor, id, monthly)
	historyCheck(t, err)
	historyEqual(t, "monthly composition uses fresh Add allocation", july.TotalCents, int64(11250))
	clock("2024-07-02")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly' AND due_on='2024-08-01'`, id).Scan(&monthly))
	august, err := svc.ReviewLeaseBillingPeriod(ctx, actor, id, monthly)
	historyCheck(t, err)
	historyEqual(t, "subsequent applicable revision governs ungenerated month", august.TotalCents, int64(12360))
	billing, err = svc.ReviewLeaseBillingRevisions(ctx, actor, id)
	historyCheck(t, err)
	historyEqual(t, "subsequent governing provenance", august.Lines[0].BillingRevisionID, billing.Revision)
}

func TestLeaseBillingRevisionCoverageReschedule(t *testing.T) {
	testLeaseBillingRevisionCoverageReschedule(t, "2024-02-01", "active")
}

func TestLeaseBillingRevisionScheduledCoverageCorrection(t *testing.T) {
	testLeaseBillingRevisionCoverageReschedule(t, "2024-01-01", "scheduled")
}

func testLeaseBillingRevisionCoverageReschedule(t *testing.T, finalizedOn, storedState string) {
	for _, action := range []string{"revise", "correct"} {
		for _, date := range []string{"2024-03-10", "2024-03-01"} {
			t.Run(action+"/"+date, func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				svc := authorization.New(pool)
				actor := authorization.Actor{UserID: admin}
				clock := func(date string) {
					t.Helper()
					_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+date+`'::date $$`)
					historyCheck(t, err)
				}
				clock(finalizedOn)
				cookie := sessionCookie(t, ctx, sessions, admin)
				customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reschedule customer").SaveX(ctx)
				location := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(customer.ID).SetTitle("Premises").SaveX(ctx)
				typeRow := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
				assetRow := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typeRow.ID).SetName("Original allocation").SaveX(ctx)
				asset, typ := assetRow.ID, typeRow.ID
				lease, err := svc.RegisterDraftLease(ctx, actor, customer.ID)
				historyCheck(t, err)
				id := lease.ID
				historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, id, authorization.DraftLeaseDetailsInput{StartsOn: "2024-01-31", EndsOn: "2024-12-31", PremisesID: &location.ID, AssetIDs: []int64{asset}}))
				initial, rent := int64(101), int64(5000)
				historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, id, 0, "2024-01-31", authorization.DraftLeaseTerms{AssetID: asset, InitialCents: &initial, RentCents: &rent, TaxRate: "5"}, 0))
				historyCheck(t, svc.FinalizeLease(ctx, actor, id, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: "2024-01-31"}))
				var persisted string
				historyCheck(t, pool.QueryRow(ctx, `SELECT lifecycle FROM leases WHERE id=$1`, id).Scan(&persisted))
				historyEqual(t, "finalized lifecycle", persisted, storedState)
				incoming := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Rescheduled incoming allocation").SaveX(ctx)
				_, err = svc.ChangeLeaseCoverage(ctx, actor, id, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "replace", EffectiveOn: "2024-02-20", OutgoingID: asset, IncomingID: incoming.ID}, Action: "create", Reason: "Scheduled replacement", OperationKey: "reschedule-replacement-0001"})
				historyCheck(t, err)
				historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, id, authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-03-01", Reason: "Approved incoming rent", OperationKey: "reschedule-incoming-rent-0001", Changes: []authorization.LeaseBillingChange{{AssetID: incoming.ID, RentCents: 6000, TaxRate: "5"}}}))
				if action == "correct" {
					clock("2024-03-20")
					current, err := svc.ReviewLeaseCoverage(ctx, actor, id)
					historyCheck(t, err)
					historyEqual(t, "time passage derives Active without a worker", current.Base.Lifecycle, "active")
					historyCheck(t, pool.QueryRow(ctx, `SELECT lifecycle FROM leases WHERE id=$1`, id).Scan(&persisted))
					historyEqual(t, "read does not persist a lifecycle transition", persisted, storedState)
					historyCheck(t, svc.GenerateLeaseDrafts(ctx))
				}
				review, err := svc.ReviewLeaseCoverage(ctx, actor, id)
				historyCheck(t, err)
				event := review.Events[0]
				values := url.Values{"action": {action}, "kind": {"replace"}, "effective_on": {date}, "outgoing_id": {fmt.Sprint(asset)}, "incoming_id": {fmt.Sprint(incoming.ID)}, "reason": {"Retain date correction proposal"}, "operation_key": {"reschedule-date-change-0001"}, "expected_revision": {fmt.Sprint(review.Revision)}, "revision": {fmt.Sprint(event.Revision)}, "event_id": {fmt.Sprint(event.ID)}}
				tables := []string{"lease_coverage_events", "lease_coverage_revisions", "lease_asset_dependencies", "lease_billing_revisions", "lease_billing_source_changes", "lease_invoice_review_flags", "invoices", "lease_invoice_generations", "authorization_audit"}
				before := leasePreservationRows(t, pool, tables)
				snapshots := historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations", "lease_finalizations"})
				path := fmt.Sprintf("/leases/%d/coverage", id)
				response := fieldPost(t, web, cookie, path, values, 409)
				assertContains(t, response.Body.String(), "approved billing revision")
				assertContains(t, response.Body.String(), "Retain date correction proposal")
				historyEqual(t, "rejected reschedule is atomic", leasePreservationRows(t, pool, tables), before)
				// Keep the incoming allocation before its approved revision, then replay
				// the exact accepted command without another history or source effect.
				values.Set("effective_on", "2024-02-29")
				_, err = pool.Exec(ctx, `CREATE FUNCTION reject_succession_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'lease.coverage.%' THEN RAISE EXCEPTION 'injected succession audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_succession_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_succession_audit()`)
				historyCheck(t, err)
				fieldPost(t, web, cookie, path, values, 409)
				historyEqual(t, "late acceptance failure rolls back all effects", leasePreservationRows(t, pool, tables), before)
				_, err = pool.Exec(ctx, `DROP TRIGGER reject_succession_audit ON authorization_audit`)
				historyCheck(t, err)
				fieldPost(t, web, cookie, path, values, 303)
				accepted := leasePreservationRows(t, pool, tables)
				fieldPost(t, web, cookie, path, values, 303)
				historyEqual(t, "accepted reschedule replay", leasePreservationRows(t, pool, tables), accepted)
				historyCheck(t, pool.QueryRow(ctx, `SELECT l.lifecycle||':'||d.lifecycle FROM leases l JOIN lease_master_dependencies d ON d.lease_id=l.id WHERE l.id=$1`, id).Scan(&persisted))
				historyEqual(t, "coverage correction preserves stored lifecycle agreement", persisted, storedState+":"+storedState)
				historyEqual(t, "accepted coverage edit preserves generated snapshots", historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations", "lease_finalizations"}), snapshots)
				if date == "2024-03-10" {
					t.Run("browser recovery", func(t *testing.T) {
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
						output, err := exec.CommandContext(browserCtx, "node", "testdata/browser/lease-billing-succession.cjs", server.URL, cookie.Value, fmt.Sprint(id), fmt.Sprint(event.ID)).CombinedOutput()
						t.Logf("succession browser: %s", output)
						historyCheck(t, err)
					})
				}
				clock("2024-04-02")
				historyCheck(t, svc.GenerateLeaseDrafts(ctx))
				var obligation int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly' AND due_on='2024-05-01'`, id).Scan(&obligation))
				period, err := svc.ReviewLeaseBillingPeriod(ctx, actor, id, obligation)
				historyCheck(t, err)
				historyEqual(t, "approved incoming full rent and tax", period.TotalCents, int64(6300))
				billing, err := svc.ReviewLeaseBillingRevisions(ctx, actor, id)
				historyCheck(t, err)
				historyEqual(t, "governing incoming revision", period.Lines[0].BillingRevisionID, billing.Revision)
			})
		}
	}
}
