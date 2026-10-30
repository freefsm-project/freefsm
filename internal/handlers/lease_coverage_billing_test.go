package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseCoverageBillingHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	clock := func(date string) {
		t.Helper()
		_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+date+`'::date $$`)
		historyCheck(t, err)
	}
	clock("2024-01-01")
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Coverage billing").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	var assets []int64
	for _, name := range []string{"Original", "Replacement", "Added"} {
		assets = append(assets, client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName(name).SaveX(ctx).ID)
	}
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	lease, err := svc.RegisterDraftLease(ctx, actor, c.ID)
	historyCheck(t, err)
	historyCheck(t, svc.SaveDraftLeaseDetails(ctx, actor, lease.ID, authorization.DraftLeaseDetailsInput{StartsOn: "2024-01-01", EndsOn: "2024-12-31", PremisesID: &l.ID, AssetIDs: assets[:1]}))
	zero, rent := int64(0), int64(3200)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, actor, lease.ID, 0, "2024-01-01", authorization.DraftLeaseTerms{AssetID: assets[0], InitialCents: &zero, RentCents: &rent, TaxRate: "0"}, 0))
	historyCheck(t, svc.FinalizeLease(ctx, actor, lease.ID, authorization.LeaseFinalizationInput{DetailsRevision: 1, BillingRevision: 1, ManagedBillingStart: "2024-01-01"}))
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/leases/%d/coverage", lease.ID)
	change := func(values url.Values) {
		t.Helper()
		review, err := svc.ReviewLeaseCoverage(ctx, actor, lease.ID)
		historyCheck(t, err)
		values.Set("expected_revision", fmt.Sprint(review.Revision))
		values.Set("action", "create")
		values.Set("reason", "Approved equipment change")
		fieldPost(t, web, cookie, path, values, 303)
	}
	change(url.Values{"kind": {"replace"}, "effective_on": {"2024-02-10"}, "outgoing_id": {fmt.Sprint(assets[0])}, "incoming_id": {fmt.Sprint(assets[1])}, "operation_key": {"billing-replace-0001"}})
	change(url.Values{"kind": {"add"}, "effective_on": {"2024-02-20"}, "incoming_id": {fmt.Sprint(assets[2])}, "rent_cents": {"1700"}, "charge_cents": {"500"}, "tax_rate": {"0"}, "operation_key": {"billing-add-0000001"}})
	change(url.Values{"kind": {"remove"}, "effective_on": {"2024-03-10"}, "outgoing_id": {fmt.Sprint(assets[2])}, "operation_key": {"billing-remove-0001"}})
	clock("2024-02-01")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	check := func(kind, due string, expected []int64, total string) {
		t.Helper()
		var ids []int64
		var amount string
		historyCheck(t, pool.QueryRow(ctx, `SELECT ARRAY(SELECT (x->>'asset_id')::bigint FROM jsonb_array_elements(i.line_items) x ORDER BY (x->>'asset_id')::bigint),i.financial::jsonb->'result'->>'total_cents' FROM lease_billing_obligations o JOIN lease_invoice_generations g ON g.obligation_id=o.id JOIN invoices i ON i.id=g.invoice_id WHERE o.lease_id=$1 AND o.kind=$2 AND o.due_on=$3::date`, lease.ID, kind, due).Scan(&ids, &amount))
		historyEqual(t, kind+due+" provenance", ids, expected)
		historyEqual(t, kind+due+" amount", amount, total)
	}
	check("monthly", "2024-02-01", assets[:1], "3200")
	check("monthly", "2024-03-01", assets[1:], "4900")
	check("coverage", "2024-02-20", assets[2:], "500")
	clock("2024-03-02")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	check("monthly", "2024-04-01", assets[1:2], "3200")
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var duplicates bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT obligation_id FROM lease_invoice_generations GROUP BY obligation_id HAVING count(*)>1)`).Scan(&duplicates))
	historyEqual(t, "worker replay", duplicates, false)
}
