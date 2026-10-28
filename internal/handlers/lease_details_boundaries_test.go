package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseNewSelectionAuthorityAndStaleIntentHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Selection Customer").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Valid premises").SaveX(ctx)
	wrong := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(other.ID).SetTitle("Other premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Intended Asset").SaveX(ctx)
	owned := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(typ.ID).SetName("Not Company-owned").SaveX(ctx)
	fieldPost(t, web, adminCookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Base without master views", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Base editor").SetEmail("lease-select65@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "premises_id": {fmt.Sprint(l.ID)}}, 404)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "asset_ids": {fmt.Sprint(a.ID)}}, 404)
	fieldPost(t, web, adminCookie, path, url.Values{"revision": {"0"}, "premises_id": {fmt.Sprint(wrong.ID)}}, 404)
	fieldPost(t, web, adminCookie, path, url.Values{"revision": {"0"}, "asset_ids": {fmt.Sprint(owned.ID)}}, 409)
	fieldPost(t, web, adminCookie, path, url.Values{"revision": {"0"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	// Draft intent does not block Asset archive; retained intent is allowed.
	fieldPost(t, web, adminCookie, fmt.Sprintf("/assets/%d/delete", a.ID), nil, 303)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"1"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}, "title": {"Retained intent"}}, 303)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"2"}}, 303)
	fieldPost(t, web, adminCookie, path, url.Values{"revision": {"3"}, "asset_ids": {fmt.Sprint(a.ID)}}, 409)
	// Clearing the premises releases the existing Location archive guard.
	fieldPost(t, web, adminCookie, fmt.Sprintf("/locations/%d/delete", l.ID), nil, 303)
	fieldPost(t, web, adminCookie, path, url.Values{"revision": {"3"}, "premises_id": {fmt.Sprint(l.ID)}}, 409)
}

func TestLeaseAgreementFileBoundParentAndIndependentGrantHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Agreement files").SaveX(ctx)
	for range 2 {
		fieldPost(t, web, adminCookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	}
	var ids []int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM leases`).Scan(&ids))
	uploadRouter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		q.Set("object_type", "lease")
		r.URL.RawQuery = q.Encode()
		web.ServeHTTP(w, r)
	})
	for _, id := range ids {
		response := uploadFileHTTP(t, uploadRouter, adminCookie, id, "agreement.txt", []byte("External agreement"))
		if response.Code != 200 {
			t.Fatalf("upload: %d %s", response.Code, response.Body.String())
		}
	}
	var files []int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY object_id) FROM files`).Scan(&files))
	path := fmt.Sprintf("/leases/%d", ids[0])
	fieldPost(t, web, adminCookie, path, url.Values{"revision": {"0"}, "agreement_file_id": {fmt.Sprint(files[1])}}, 404)
	fieldPost(t, web, adminCookie, path, url.Values{"revision": {"0"}, "agreement_file_id": {fmt.Sprint(files[0])}}, 303)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "No download", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}, {Permission: "lease.manage", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("No download editor").SetEmail("lease-file65@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	body := requestBody(t, web, cookie, "GET", path, 200)
	if strings.Contains(body, `name="agreement_file_id"`) || strings.Contains(body, fmt.Sprintf(`href="/files/%d"`, files[0])) {
		t.Fatal("agreement access exposed without File Download")
	}
	fieldPost(t, web, cookie, path, url.Values{"revision": {"1"}, "agreement_file_id": {fmt.Sprint(files[0])}}, 403)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"1"}, "title": {"Unrelated edit"}}, 303)
	var selected int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT agreement_file_id FROM leases WHERE id=$1`, ids[0]).Scan(&selected))
	if selected != files[0] {
		t.Fatal("unrelated edit silently cleared inaccessible agreement selection")
	}
	requestBody(t, web, cookie, "GET", fmt.Sprintf("/files/%d", files[0]), 403)
	if _, err := pool.Exec(ctx, `UPDATE files SET object_id=$2 WHERE id=$1`, files[0], ids[1]); err == nil {
		t.Fatal("Lease File moved from original parent")
	}
}

func TestLeaseBillingAuditFailureAndBaseRemovalHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Prospective terms").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Intended").SaveX(ctx)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	values := url.Values{"revision": {"0"}, "asset_id": {fmt.Sprint(a.ID)}, "rent_cents": {"200"}}
	fieldPost(t, web, cookie, path+"/billing", values, 303)
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_lease_billing_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.billing.change' THEN RAISE EXCEPTION 'injected billing audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_lease_billing_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_lease_billing_audit()`)
	historyCheck(t, err)
	tables := []string{"leases", "lease_draft_billing", "lease_draft_asset_terms", "authorization_audit"}
	before := historyDurableRows(t, pool, tables)
	values.Set("revision", "1")
	values.Set("rent_cents", "999")
	fieldPost(t, web, cookie, path+"/billing", values, 409)
	historyEqual(t, "billing audit failure is atomic", historyDurableRows(t, pool, tables), before)
	_, err = pool.Exec(ctx, `DROP TRIGGER reject_lease_billing_audit ON authorization_audit`)
	historyCheck(t, err)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"1"}}, 303)
	fieldPost(t, web, cookie, path+"/billing", values, 409)
	body := requestBody(t, web, cookie, "GET", path+"/billing", 200)
	assertContains(t, body, "retained terms")
	assertContains(t, body, "200")
	var clean bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM invoices) AND NOT EXISTS(SELECT 1 FROM lease_asset_dependencies)`).Scan(&clean))
	if !clean {
		t.Fatal("Draft terms acquired operational effects")
	}
}
