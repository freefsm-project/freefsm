package authorization_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func assertDiscoveryLeaseTerm(t *testing.T, f leaseExportProjectionFixture, actor authorization.Actor, text string, want int) {
	t.Helper()
	p := discover(t, f.commercialReportFixture, actor, authorization.DiscoveryQuery{Types: []string{"lease"}, Search: text})
	if p.Total != want {
		t.Fatalf("Lease term %q: want %d, got %+v", text, want, p)
	}
}

func discoveryReviseRent(t *testing.T, f leaseExportProjectionFixture, asset, cents int64, day time.Time, rate string) {
	t.Helper()
	review, err := f.svc.ReviewLeaseBillingRevisions(context.Background(), f.actor, f.lease)
	if err != nil {
		t.Fatal(err)
	}
	err = f.svc.ReviseLeaseBilling(context.Background(), f.actor, f.lease, authorization.LeaseBillingRevisionInput{
		ExpectedRevision: review.Revision, EffectiveOn: day.Format(time.DateOnly),
		Reason: "Discovery effective term regression", OperationKey: fmt.Sprintf("discovery-billing-%d-%d", asset, cents),
		Changes: []authorization.LeaseBillingChange{{AssetID: asset, RentCents: cents, TaxRate: rate}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveryLeaseEffectiveBillingRevision(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	billing := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "lease.billing.view", Scope: authorization.All})
	base := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "lease.view", Scope: authorization.All})
	assertDiscoveryLeaseTerm(t, f, billing, "123.45", 1)
	discoveryReviseRent(t, f, f.asset, 87654, f.today, "17.321")
	assertDiscoveryLeaseTerm(t, f, billing, "876.54", 1)
	assertDiscoveryLeaseTerm(t, f, billing, "17.321", 1)
	assertDiscoveryLeaseTerm(t, f, billing, "123.45", 0)
	assertDiscoveryLeaseTerm(t, f, base, "876.54", 0)
	assertDiscoveryLeaseTerm(t, f, base, "17.321", 0)
	discoveryReviseRent(t, f, f.asset, 76543, f.today.AddDate(0, 0, 1), "18.432")
	assertDiscoveryLeaseTerm(t, f, billing, "876.54", 1)
	assertDiscoveryLeaseTerm(t, f, billing, "765.43", 0)
	assertDiscoveryLeaseTerm(t, f, billing, "18.432", 0)
}

func discoveryCoverageChange(t *testing.T, f leaseExportProjectionFixture, event authorization.LeaseCoverageEvent, rent *int64) {
	t.Helper()
	review, err := f.svc.ReviewLeaseCoverage(context.Background(), f.actor, f.lease)
	if err != nil {
		t.Fatal(err)
	}
	rate := ""
	if rent != nil {
		rate = "5"
	}
	_, err = f.svc.ChangeLeaseCoverage(context.Background(), f.actor, f.lease, authorization.LeaseCoverageInput{
		LeaseCoverageEvent: event, ExpectedRevision: review.Revision, Action: "create",
		Reason:       "Discovery coverage allocation regression",
		OperationKey: fmt.Sprintf("discovery-coverage-%d-%s-%d-%d", review.Revision, event.Kind, event.IncomingID, event.OutgoingID),
		RentCents:    rent, TaxRate: rate,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func discoveryLeaseAsset(t *testing.T, f leaseExportProjectionFixture) int64 {
	t.Helper()
	var id int64
	err := f.pool.QueryRow(context.Background(), `INSERT INTO assets(company_id,asset_type_id,ownership_class,name)
 SELECT company_id,asset_type_id,'company_owned','Discovery allocation asset' FROM assets WHERE id=$1 RETURNING id`, f.asset).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestDiscoveryLeaseCoverageAllocationTerms(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	billing := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "lease.billing.view", Scope: authorization.All})
	base := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "lease.view", Scope: authorization.All})
	added, replacement, future := discoveryLeaseAsset(t, f), discoveryLeaseAsset(t, f), discoveryLeaseAsset(t, f)
	rent := int64(65432)
	discoveryCoverageChange(t, f, authorization.LeaseCoverageEvent{
		Kind: "add", EffectiveOn: f.today.AddDate(0, 0, -3).Format(time.DateOnly), IncomingID: added,
	}, &rent)
	assertDiscoveryLeaseTerm(t, f, billing, "654.32", 1)
	assertDiscoveryLeaseTerm(t, f, base, "654.32", 0)
	discoveryReviseRent(t, f, added, 76543, f.today.AddDate(0, 0, -2), "5")
	discoveryCoverageChange(t, f, authorization.LeaseCoverageEvent{
		Kind: "replace", EffectiveOn: f.today.AddDate(0, 0, -1).Format(time.DateOnly), OutgoingID: added, IncomingID: replacement,
	}, nil)
	assertDiscoveryLeaseTerm(t, f, billing, "765.43", 1)
	assertDiscoveryLeaseTerm(t, f, billing, "654.32", 0)
	discoveryCoverageChange(t, f, authorization.LeaseCoverageEvent{
		Kind: "remove", EffectiveOn: f.today.Format(time.DateOnly), OutgoingID: replacement,
	}, nil)
	assertDiscoveryLeaseTerm(t, f, billing, "765.43", 0)
	rent = 54321
	discoveryCoverageChange(t, f, authorization.LeaseCoverageEvent{
		Kind: "add", EffectiveOn: f.today.Format(time.DateOnly), IncomingID: added,
	}, &rent)
	assertDiscoveryLeaseTerm(t, f, billing, "543.21", 1)
	assertDiscoveryLeaseTerm(t, f, billing, "765.43", 0)
	rent = 43210
	discoveryCoverageChange(t, f, authorization.LeaseCoverageEvent{
		Kind: "add", EffectiveOn: f.today.AddDate(0, 0, 1).Format(time.DateOnly), IncomingID: future,
	}, &rent)
	assertDiscoveryLeaseTerm(t, f, billing, "432.10", 0)
	review, err := f.svc.ReviewLeaseCoverage(context.Background(), f.actor, f.lease)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range review.Events {
		if event.IncomingID != future {
			continue
		}
		input := event.LeaseCoverageInput
		input.Action, input.EffectiveOn = "revise", f.today.Format(time.DateOnly)
		input.ExpectedRevision = review.Revision
		input.OperationKey = "discovery-revise-pending-coverage"
		input.Reason = "Coverage effective today after review"
		if _, err := f.svc.ChangeLeaseCoverage(context.Background(), f.actor, f.lease, input); err != nil {
			t.Fatal(err)
		}
	}
	assertDiscoveryLeaseTerm(t, f, billing, "432.10", 1)
}

func TestDiscoveryLeaseDraftTermsRemainSearchable(t *testing.T) {
	f := newLeaseExportProjectionFixture(t)
	asset := discoveryLeaseAsset(t, f)
	ctx := context.Background()
	draft, err := f.svc.RegisterDraftLease(ctx, f.actor, f.customer)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SaveDraftLeaseDetails(ctx, f.actor, draft.ID, authorization.DraftLeaseDetailsInput{
		Title: "Discovery Draft", AssetIDs: []int64{asset},
	}); err != nil {
		t.Fatal(err)
	}
	initial, rent := int64(24680), int64(13579)
	if err := f.svc.SaveDraftLeaseBilling(ctx, f.actor, draft.ID, 0, f.today.Format(time.DateOnly),
		authorization.DraftLeaseTerms{AssetID: asset, InitialCents: &initial, RentCents: &rent, TaxRate: "9.876"}, 0); err != nil {
		t.Fatal(err)
	}
	billing := discoveryActor(t, f.commercialReportFixture, authorization.Grant{Permission: "lease.billing.view", Scope: authorization.All})
	for _, text := range []string{"246.80", "135.79", "9.876"} {
		assertDiscoveryLeaseTerm(t, f, billing, text, 1)
	}
}
