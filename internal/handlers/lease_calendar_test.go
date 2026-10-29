package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
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
			assertContains(t, requestBody(t, web, cookie, "GET", path, 200), tc.state)
		})
	}
	var state string
	var invoices int
	historyCheck(t, pool.QueryRow(ctx, `SELECT lifecycle,(SELECT count(*) FROM invoices) FROM leases WHERE id=$1`, id).Scan(&state, &invoices))
	if state != "scheduled" || invoices != 1 {
		t.Fatalf("reads changed saved state or generated billing: %s / %d", state, invoices)
	}
	if _, err := svc.DraftLeaseBase(ctx, authorization.Actor{UserID: admin, Channel: "api"}, id); err != authorization.ErrNotFound {
		t.Fatalf("field API exposed Lease calendar: %v", err)
	}
}
