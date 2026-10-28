package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestLeaseRestoreRetainsArchivedPremisesWithoutRevivalHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("State-only restoration").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Retained premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Intended history").SaveX(ctx)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	values := url.Values{"revision": {"0"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}, "title": {"Retained Draft"}}
	fieldPost(t, web, cookie, path, values, 303)
	fieldPost(t, web, cookie, path+"/delete", nil, 303)
	body := requestBody(t, web, cookie, "GET", path, 200)
	assertContains(t, body, "Archived")
	if strings.Contains(body, "Save Draft details") {
		t.Fatal("archived details remained editable")
	}
	fieldPost(t, web, cookie, fmt.Sprintf("/locations/%d/delete", l.ID), nil, 303)
	fieldPost(t, web, cookie, fmt.Sprintf("/assets/%d/delete", a.ID), nil, 303)
	client.CustomFieldDefinition.Create().SetCompanyID(company).SetObjectType("lease").SetName("New required on details, not restore").SetFieldType("text").SetRequired(true).SetDefaultValue("no backfill").SaveX(ctx)
	fieldPost(t, web, cookie, path+"/restore", nil, 303)
	var unchanged bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT l.deleted_at IS NULL AND l.premises_id=$2 AND l.custom_fields='[]'::jsonb AND EXISTS(SELECT 1 FROM locations WHERE id=$2 AND deleted_at IS NOT NULL) AND EXISTS(SELECT 1 FROM assets WHERE id=$3 AND deleted_at IS NOT NULL AND customer_id IS NULL) AND EXISTS(SELECT 1 FROM lease_intended_assets WHERE lease_id=l.id AND asset_id=$3) AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies WHERE lease_id=l.id) AND NOT EXISTS(SELECT 1 FROM invoices) FROM leases l WHERE id=$1`, id, l.ID, a.ID).Scan(&unchanged))
	if !unchanged {
		t.Fatal("Restore revived or rewrote retained history/dependencies/defaults")
	}
	body = requestBody(t, web, cookie, "GET", path, 200)
	assertContains(t, body, "Archived Location")
	assertContains(t, body, "Archived Asset")
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/billing", 200), "Archived Asset")
	if _, err := pool.Exec(ctx, `SELECT set_lease_master_dependencies($1,$2,$3,$4,'scheduled',false,'[]')`, id, company, c.ID, l.ID); err == nil || !strings.Contains(err.Error(), "active Customer and premises") {
		t.Fatalf("future finalization adapter accepted stale premises: %v", err)
	}
	// Another agreement cannot select the archived Location as NEW intent.
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var other int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT max(id) FROM leases`).Scan(&other))
	fieldPost(t, web, cookie, fmt.Sprintf("/leases/%d", other), url.Values{"revision": {"0"}, "premises_id": {fmt.Sprint(l.ID)}}, 409)
}
