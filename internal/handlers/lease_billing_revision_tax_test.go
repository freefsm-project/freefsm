package handlers

import (
	"context"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseBillingRevisionTaxSchedule(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	lease, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	var asset, typ int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT a.id,a.asset_type_id FROM assets a JOIN lease_asset_dependencies d ON d.asset_id=a.id WHERE d.lease_id=$1`, lease).Scan(&asset, &typ))
	added := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Second tax allocation").SaveX(ctx)
	rent := int64(1000)
	_, err = svc.ChangeLeaseCoverage(ctx, actor, lease, authorization.LeaseCoverageInput{LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", EffectiveOn: "2024-02-10", IncomingID: added.ID}, RentCents: &rent, TaxRate: "5.000", Action: "create", Reason: "Compatible additional equipment", OperationKey: "billing-tax-add-000001"})
	historyCheck(t, err)
	in := authorization.LeaseBillingRevisionInput{EffectiveOn: "2024-03-01", Reason: "Change whole taxable schedule", OperationKey: "billing-tax-mismatch-0001", Changes: []authorization.LeaseBillingChange{{AssetID: asset, RentCents: 6000, TaxRate: "7"}}}
	before := leasePreservationRows(t, pool, []string{"lease_billing_revisions", "lease_billing_source_changes", "lease_invoice_review_flags", "authorization_audit"})
	if err = svc.ReviseLeaseBilling(ctx, actor, lease, in); err == nil {
		t.Fatal("incompatible future rates accepted")
	}
	historyEqual(t, "incompatible batch rolls back all effects", leasePreservationRows(t, pool, []string{"lease_billing_revisions", "lease_billing_source_changes", "lease_invoice_review_flags", "authorization_audit"}), before)
	in.Changes = append(in.Changes, authorization.LeaseBillingChange{AssetID: added.ID, RentCents: 1000, TaxRate: "7.000"})
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, lease, in))
	review, err := svc.ReviewLeaseBillingRevisions(ctx, actor, lease)
	historyCheck(t, err)
	in.ExpectedRevision = review.Revision
	in.EffectiveOn = "2024-06-01"
	in.OperationKey = "billing-tax-future-0001"
	in.Changes[0].TaxRate = "9"
	in.Changes[1].TaxRate = "9.000"
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, lease, in))
	third := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ).SetName("Future conflicting allocation").SaveX(ctx)
	coverage, err := svc.ReviewLeaseCoverage(ctx, actor, lease)
	historyCheck(t, err)
	_, err = svc.ChangeLeaseCoverage(ctx, actor, lease, authorization.LeaseCoverageInput{ExpectedRevision: coverage.Revision, LeaseCoverageEvent: authorization.LeaseCoverageEvent{Kind: "add", EffectiveOn: "2024-04-10", IncomingID: third.ID}, RentCents: &rent, TaxRate: "7", Action: "create", Reason: "Conflicts with later approved tax", OperationKey: "billing-tax-future-add-0001"})
	if err == nil {
		t.Fatal("coverage accepted tax conflict at later revision boundary")
	}
	// Exemption removes effective rate conflict but never relaxes raw validity.
	var customer int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id FROM leases WHERE id=$1`, lease).Scan(&customer))
	client.Customer.UpdateOneID(customer).SetTaxExempt(true).SaveX(ctx)
	review, err = svc.ReviewLeaseBillingRevisions(ctx, actor, lease)
	historyCheck(t, err)
	in.ExpectedRevision = review.Revision
	in.EffectiveOn = "2024-07-01"
	in.OperationKey = "billing-tax-exempt-0001"
	in.Changes = in.Changes[:1]
	in.Changes[0].TaxRate = "8.123456789"
	historyCheck(t, svc.ReviseLeaseBilling(ctx, actor, lease, in))
	review, err = svc.ReviewLeaseBillingRevisions(ctx, actor, lease)
	historyCheck(t, err)
	in.ExpectedRevision = review.Revision
	in.OperationKey = "billing-tax-invalid-0001"
	in.Changes[0].TaxRate = "101"
	if err = svc.ReviseLeaseBilling(ctx, actor, lease, in); err == nil {
		t.Fatal("exemption accepted invalid raw tax")
	}
}
