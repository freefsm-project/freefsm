package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"
)

func TestLeaseDraftTaxPrecisionHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Exact Draft tax").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Intended").SaveX(ctx)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	values := url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(a.ID)}, "tax_rate": {"7.123456789"}}
	fieldPost(t, web, cookie, path+"/billing", values, 303)
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/billing", 200), "7.123456789")
	before := historyDurableRows(t, pool, []string{"lease_draft_billing", "lease_draft_asset_terms", "authorization_audit"})
	values.Set("revision", "1")
	for _, rate := range []string{"101", "-1", "NaN", "1e2"} {
		values.Set("tax_rate", rate)
		fieldPost(t, web, cookie, path+"/billing", values, 409)
	}
	historyEqual(t, "invalid tax leaves exact prospective terms and audit intact", historyDurableRows(t, pool, []string{"lease_draft_billing", "lease_draft_asset_terms", "authorization_audit"}), before)
}
