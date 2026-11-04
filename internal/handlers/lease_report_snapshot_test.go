package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseReportHistoricalDraftHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Historical Draft Customer").SaveX(ctx)
	// Seed yesterday's accepted history at insertion, never rewrite retained
	// events. Subsequent public edits use the ordinary recording clock.
	_, err := pool.Exec(ctx, `CREATE FUNCTION report_fixture_created() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.created_at=clock_timestamp()-interval '1 day'; RETURN NEW; END $$;
 CREATE FUNCTION report_fixture_recorded() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.recorded_at=clock_timestamp()-interval '1 day'; RETURN NEW; END $$;
 CREATE TRIGGER report_fixture_created BEFORE INSERT ON leases FOR EACH ROW EXECUTE FUNCTION report_fixture_created();
 CREATE TRIGGER report_fixture_recorded BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION report_fixture_recorded();`)
	historyCheck(t, err)
	lease, err := svc.RegisterDraftLease(ctx, actor, customer.ID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Title: "Yesterday Draft title"}))
	_, err = pool.Exec(ctx, `DROP TRIGGER report_fixture_created ON leases; DROP TRIGGER report_fixture_recorded ON authorization_audit;`)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{Revision: 1, Title: "Today Draft title"}))
	var yesterday string
	historyCheck(t, pool.QueryRow(ctx, `SELECT (lease_dependency_today($1)-1)::text`, company).Scan(&yesterday))
	body := requestBody(t, web, cookie, "GET", "/leases/report?as_of="+yesterday, 200)
	assertContains(t, body, "Yesterday Draft title")
	assertNotContains(t, body, "Today Draft title")
	assertContains(t, requestBody(t, web, cookie, "GET", "/leases/report?reset=1", 200), "Today Draft title")
}

func TestLeaseReportConcurrentBillingAndCorrectionHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	base, err := svc.DraftLeaseBase(ctx, actor, lease)
	historyCheck(t, err)
	var asset int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$`, base.StartsOn))
	historyCheck(t, err)
	start, err := time.Parse(time.DateOnly, base.StartsOn)
	historyCheck(t, err)
	for _, change := range []func() error{
		func() error {
			return svc.ReviseLeaseBilling(ctx, actor, lease, authorization.LeaseBillingRevisionInput{EffectiveOn: base.StartsOn, Reason: "Concurrent rent", OperationKey: newOperationKey(), Changes: []authorization.LeaseBillingChange{{AssetID: asset, RentCents: 7500, TaxRate: "5"}}})
		},
		func() error {
			return svc.CorrectLeaseFacts(ctx, actor, lease, authorization.LeaseFactInput{ExpectedRevision: base.Revision, StartsOn: start.AddDate(0, 0, 1).Format(time.DateOnly), EndsOn: base.EndsOn, PremisesID: *base.PremisesID, Factual: true, Reason: "Concurrent commencement correction", OperationKey: newOperationKey()})
		},
	} {
		gate := make(chan struct{})
		done := make(chan error, 1)
		go func() { <-gate; done <- change() }()
		close(gate)
		for range 6 {
			r := httptest.NewRequest("GET", "/leases/report", nil)
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			web.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("concurrent report: %d", w.Code)
			}
			body := w.Body.String()
			matched := false
			for _, cents := range []string{"50.00", "75.00", "0.00"} {
				if strings.Contains(body, "Matching monthly rent: "+cents+" ") && strings.Contains(body, "Monthly rent: "+cents+"</p>") {
					matched = true
				}
			}
			if !matched {
				t.Fatal("report mixed matching total and row rent across a domain commit")
			}
		}
		historyCheck(t, <-done)
	}
	body := requestBody(t, web, cookie, "GET", "/leases/report", 200)
	assertContains(t, body, "Lifecycle: scheduled")
	assertContains(t, body, "Matching monthly rent: 0.00")
	assertContains(t, body, "Scheduled monthly rent: 75.00")
}

func TestLeaseReportRetainedUnowedCoverageHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	base, err := svc.DraftLeaseBase(ctx, actor, lease)
	historyCheck(t, err)
	start, err := time.Parse(time.DateOnly, base.StartsOn)
	historyCheck(t, err)
	due := start.AddDate(0, 0, 5).Format(time.DateOnly)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Unowed report equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Withdrawn optional charge Asset").SaveX(ctx)
	rent, charge := int64(1000), int64(100)
	_, err = svc.ChangeLeaseCoverage(ctx, actor, lease, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", EffectiveOn: due, IncomingID: asset.ID}, Action: "create", Reason: "Pending optional charge", OperationKey: newOperationKey(), RentCents: &rent, ChargeCents: &charge, TaxRate: "5"})
	historyCheck(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$;
 CREATE FUNCTION report_fail_optional() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.line_items::jsonb->0->>'charge_kind'='coverage' THEN RAISE EXCEPTION 'report fixture generation failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER report_fail_optional BEFORE INSERT ON invoices FOR EACH ROW EXECUTE FUNCTION report_fail_optional();`, base.StartsOn))
	historyCheck(t, err)
	if err := svc.GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("expected optional generation failure")
	}
	review, err := svc.ReviewLeaseCoverage(ctx, actor, lease)
	historyCheck(t, err)
	withdraw := review.Events[0].LeaseCoverageInput
	withdraw.ExpectedRevision, withdraw.Action, withdraw.Reason, withdraw.OperationKey = review.Revision, "withdraw", "Charge never commenced", newOperationKey()
	_, err = svc.ChangeLeaseCoverage(ctx, actor, lease, withdraw)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER report_fail_optional ON invoices; CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$; DELETE FROM lease_billing_period_dispositions WHERE lease_id=%d`, due, lease))
	historyCheck(t, err)
	body := requestBody(t, web, cookie, "GET", "/leases/report", 200)
	assertContains(t, body, "coverage due "+due+": No Assets covered at period start; no charge owed")
	assertNotContains(t, body, "coverage due "+due+": ungenerated")
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{Action: "create", Category: "mutual_early", Initiator: "mutual", NoticeOn: due, EffectiveOn: due, Reason: "Inclusive end after withdrawn addition", OperationKey: newOperationKey()}))
	later := start.AddDate(0, 3, 0).Format(time.DateOnly)
	_, err = pool.Exec(ctx, fmt.Sprintf(`CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '%s'::date $$`, later))
	historyCheck(t, err)
	body = requestBody(t, web, cookie, "GET", "/leases/report", 200)
	assertContains(t, body, "not owed — termination")
	assertContains(t, body, "Lifecycle: terminated")
	assertContains(t, requestBody(t, web, cookie, "GET", "/leases/report?billing=ungenerated", 200), "0 matching Leases")
	notice, err := svc.ReviewLeaseNotice(ctx, actor, lease)
	historyCheck(t, err)
	historyCheck(t, svc.ChangeLeaseNotice(ctx, actor, lease, authorization.LeaseNoticeInput{ExpectedRevision: notice.Revision, Action: "revise", Factual: true, Category: "mutual_early", Initiator: "mutual", NoticeOn: due, EffectiveOn: later, Reason: "Correct the recorded inclusive termination date", OperationKey: newOperationKey()}))
	body = requestBody(t, web, cookie, "GET", "/leases/report?billing=ungenerated", 200)
	assertContains(t, body, "1 matching Leases")
	assertContains(t, body, "Inclusive termination date: "+later)
	assertContains(t, body, ": ungenerated")
	assertNotContains(t, body, "not owed — termination")
}
