package handlers

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseFinalizationNativeTimelineHTTP(t *testing.T) {
	f := newLeaseReviewFixture(t)
	loc := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(f.a).SetTitle("Contract premises").SaveX(f.ctx)
	wrong := f.client.Location.Create().SetCompanyID(f.company).SetObjectType("customer").SetObjectID(f.a).SetTitle("Other premises").SaveX(f.ctx)
	fieldPost(t, f.web, f.cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(f.a)}}, 303)
	var id int64
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, f.web, f.cookie, path, url.Values{"revision": {"0"}, "starts_on": {f.start}, "ends_on": {f.end}, "premises_id": {fmt.Sprint(loc.ID)}, "asset_ids": {fmt.Sprint(f.asset)}}, 303)
	amount := int64(100)
	historyCheck(t, authorization.New(f.pool).SaveDraftLeaseBilling(f.ctx, authorization.Actor{UserID: f.admin}, id, 0, f.start, authorization.DraftLeaseTerms{AssetID: f.asset, InitialCents: &amount, RentCents: &amount, TaxRate: "0"}, 0))
	fieldPost(t, f.web, f.cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {f.start}}, 303)
	before := f.snapshot(t)
	correction := f.correction()
	fieldPost(t, f.web, f.cookie, correction.path, correction.values, 409)
	historyEqual(t, "native coverage rejects historical Customer correction atomically", f.snapshot(t), before)
	assertContains(t, requestBody(t, f.web, f.cookie, "GET", path, 200), "is unplaced")
	var coverage string
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT jsonb_agg(to_jsonb(d))::text FROM lease_asset_dependencies d`).Scan(&coverage))
	assetPath := fmt.Sprintf("/assets/%d/associations/", f.asset)
	fieldPost(t, f.web, f.cookie, assetPath+"place", url.Values{"customer_id": {fmt.Sprint(f.a)}, "location_id": {fmt.Sprint(wrong.ID)}, "effective_date": {f.returned}, "reason": {"Place at actual premises"}, "revision": {"2"}}, 303)
	assertContains(t, requestBody(t, f.web, f.cookie, "GET", path, 200), "placed at different premises")
	fieldPost(t, f.web, f.cookie, assetPath+"return", url.Values{"effective_date": {f.returned}, "reason": {"Return without terminating coverage"}, "revision": {"3"}}, 303)
	assertContains(t, requestBody(t, f.web, f.cookie, "GET", path, 200), "is unplaced")
	var after string
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT jsonb_agg(to_jsonb(d))::text FROM lease_asset_dependencies d`).Scan(&after))
	historyEqual(t, "return preserves coverage", after, coverage)
	fieldPost(t, f.web, f.cookie, fmt.Sprintf("/assets/%d/delete", f.asset), nil, 409)
}
