package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5"
)

func TestLeaseCalendarReadWithoutWorkerHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Calendar").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Unplaced").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-01-30"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	svc := authorization.New(pool)
	amount := int64(100)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id, 0, "2024-01-01", authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &amount, RentCents: &amount, TaxRate: "0"}, 0))
	// The isolated fixture clock exercises successive reads of the same saved
	// agreement. No worker, lifecycle rewrite or new generation runs between reads.
	clock := func(date string) {
		_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+date+`'::date $$`)
		historyCheck(t, err)
	}
	clock("2023-12-31")
	fieldPost(t, web, cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {"2024-01-01"}}, 303)
	for _, tc := range []struct{ date, state, from, through string }{
		{"2023-12-31", "scheduled", "", ""},
		{"2024-01-01", "active", "", ""},
		{"2024-01-30", "active", "", ""},
		{"2024-01-31", "month_to_month", "2024-01-31", "2024-02-28"},
		{"2024-02-29", "month_to_month", "2024-02-29", "2024-03-30"},
		{"2024-03-31", "month_to_month", "2024-03-31", "2024-04-29"},
	} {
		t.Run(tc.date, func(t *testing.T) {
			clock(tc.date)
			base, err := svc.DraftLeaseBase(ctx, authorization.Actor{UserID: admin}, id)
			historyCheck(t, err)
			if base.Lifecycle != tc.state || base.RenewalStartsOn != tc.from || base.RenewalEndsOn != tc.through {
				t.Fatalf("calendar: %+v", base.LeaseCalendar)
			}
			if len(base.Coverage) != 1 || base.Coverage[0].AssetID != a.ID || base.Coverage[0].StartsOn != "2024-01-01" || base.Coverage[0].EndsOn != nil {
				t.Fatalf("finalized coverage contract: %+v", base.Coverage)
			}
			billing, err := svc.DraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id)
			historyCheck(t, err)
			if billing.Lifecycle != tc.state {
				t.Fatalf("billing lifecycle: %s", billing.Lifecycle)
			}
			for _, facet := range []bool{false, true} {
				list, err := svc.LeaseList(ctx, authorization.Actor{UserID: admin}, facet, "", "active", 1, 25)
				historyCheck(t, err)
				if len(list.Rows) != 1 || list.Rows[0].Lifecycle != tc.state {
					t.Fatalf("list lifecycle: %+v", list.Rows)
				}
				history, err := svc.LeaseHistory(ctx, authorization.Actor{UserID: admin}, id, facet)
				historyCheck(t, err)
				if history.Lifecycle != tc.state {
					t.Fatalf("history lifecycle: %s", history.Lifecycle)
				}
			}
			assertContains(t, requestBody(t, web, cookie, "GET", path, 200), tc.state)
		})
	}
	var state string
	var invoices int
	historyCheck(t, pool.QueryRow(ctx, `SELECT lifecycle,(SELECT count(*) FROM invoices) FROM leases WHERE id=$1`, id).Scan(&state, &invoices))
	if state != "scheduled" || invoices != 1 {
		t.Fatalf("reads changed saved state or generated billing: %s / %d", state, invoices)
	}
	// Keep PostgreSQL's real company timezone conversion, replacing only the
	// instant. UTC has reached Jan 31 while Los Angeles is still the inclusive end.
	_, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='America/Los_Angeles' WHERE company_id=$1`, company)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT ('2024-01-31 01:00:00+00'::timestamptz AT TIME ZONE coalesce((SELECT timezone FROM company_settings WHERE company_id=company),'UTC'))::date $$`)
	historyCheck(t, err)
	base, err := svc.DraftLeaseBase(ctx, authorization.Actor{UserID: admin}, id)
	historyCheck(t, err)
	if base.Lifecycle != "active" {
		t.Fatalf("company-local inclusive end: %+v", base)
	}
	_, err = pool.Exec(ctx, `UPDATE company_settings SET timezone='UTC' WHERE company_id=$1`, company)
	historyCheck(t, err)
	base, err = svc.DraftLeaseBase(ctx, authorization.Actor{UserID: admin}, id)
	historyCheck(t, err)
	if base.Lifecycle != "month_to_month" {
		t.Fatalf("UTC boundary: %+v", base)
	}
	if _, err := svc.DraftLeaseBase(ctx, authorization.Actor{UserID: admin, Channel: "api"}, id); err != authorization.ErrNotFound {
		t.Fatalf("field API exposed Lease calendar: %v", err)
	}
}

func TestLeaseCalendarRetainedTerminationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained terminated agreement").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Retained asset").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-01-30"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	// Model retained agreement facts at the existing dependency seam. This is
	// a read fixture, not a new termination command or invented billing history.
	svc := authorization.New(pool)
	through := "2024-02-15"
	amount := int64(100)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id, 0, "2024-01-01", authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &amount, RentCents: &amount, TaxRate: "0"}, 0))
	historyCheck(t, svc.WithinActor(ctx, authorization.Actor{UserID: admin}, func(tx pgx.Tx, _ authorization.Principal) error {
		if err := svc.WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, authorization.LeaseDependencies{LeaseID: id, CustomerID: c.ID, PremisesID: &l.ID, Lifecycle: "terminated", Coverage: []authorization.LeaseAssetDependency{{AssetID: a.ID, StartsOn: "2024-01-01", EndsOn: &through}}}); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE leases SET lifecycle='terminated' WHERE id=$1`, id)
		return err
	}))
	base, err := svc.DraftLeaseBase(ctx, authorization.Actor{UserID: admin}, id)
	historyCheck(t, err)
	if base.Lifecycle != "terminated" || base.RenewalStartsOn != "" || len(base.Coverage) != 1 || base.Coverage[0].EndsOn == nil || *base.Coverage[0].EndsOn != through {
		t.Fatalf("retained facts: %+v", base)
	}
	before := leaseDurableState(t, pool)
	fieldPost(t, web, cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {"2024-01-01"}}, 409)
	if err := svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id, 1, "2024-01-01", authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &amount, RentCents: &amount, TaxRate: "0"}, 0); err != authorization.ErrConflict {
		t.Fatalf("retained termination reopened for billing: %v", err)
	}
	historyEqual(t, "retained termination rejects Draft writers", leaseDurableState(t, pool), before)
	for _, suffix := range []string{"", "/billing", "/history", "/billing/history"} {
		assertContains(t, requestBody(t, web, cookie, "GET", path+suffix, 200), "terminated")
	}
	for _, suffix := range []string{"", "?facet=billing"} {
		assertContains(t, requestBody(t, web, cookie, "GET", "/leases"+suffix, 200), "terminated")
	}
}
