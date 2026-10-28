package handlers

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestCustomerCascadeExactReviewHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reviewed owner").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	contact := masterPost(t, web, cookie, base+"/contacts", url.Values{"first_name": {"Reviewed primary"}, "is_primary": {"true"}}, 303)
	masterPost(t, web, cookie, base+"/contacts", url.Values{"first_name": {"Reviewed secondary"}}, 303)
	location := masterPost(t, web, cookie, base+"/locations", url.Values{"title": {"Reviewed location"}}, 303)
	old := masterPost(t, web, cookie, base+"/locations", url.Values{"title": {"Historical location"}}, 303)
	masterPost(t, web, cookie, old+"/delete", nil, 303)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("customer_owned").SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetName("Reviewed equipment").SaveX(ctx)
	client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Excluded company equipment").SaveX(ctx)
	var review struct {
		Snapshot            string
		RequiredPermissions []string
		Children            []struct {
			Kind    string
			ID      int64
			Name    string
			Primary bool
		}
	}
	masterJSON(t, web, cookie, base+"/archive", &review)
	if !reflect.DeepEqual(review.RequiredPermissions, []string{"customer.archive", "contact.archive", "location.archive", "asset.archive"}) {
		t.Fatalf("review must enumerate independent grants: %+v", review)
	}
	if len(review.Children) != 4 || !review.Children[0].Primary || review.Children[1].Primary || review.Children[2].Primary || review.Children[3].Kind != "asset" || review.Children[3].ID != a.ID {
		t.Fatalf("incorrect reviewed cascade/primary effects: %+v", review)
	}
	body := requestBody(t, web, cookie, "GET", base+"/archive", 200)
	for _, text := range []string{"Customer-owned Assets", "Company-owned Assets are excluded", "customer.archive", "contact.archive", "location.archive", "asset.archive", "Primary default will be cleared", "Reviewed equipment"} {
		assertContains(t, body, text)
	}
	assertNotContains(t, body, "Historical location")
	assertNotContains(t, body, "Excluded company equipment")
	before := historyDurableRows(t, pool, []string{"customer_contacts", "locations", "assets", "customers", "authorization_audit"})
	// A newly included child invalidates exactly what the user reviewed.
	newContact := masterPost(t, web, cookie, base+"/contacts", url.Values{"first_name": {"Added after review"}}, 303)
	changed := historyDurableRows(t, pool, []string{"customer_contacts", "locations", "assets", "customers", "authorization_audit"})
	masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 409)
	historyEqual(t, "stale review has no effects or partial audit", historyDurableRows(t, pool, []string{"customer_contacts", "locations", "assets", "customers", "authorization_audit"}), changed)
	if reflect.DeepEqual(before, changed) {
		t.Fatal("scope challenge did not add a child")
	}
	masterJSON(t, web, cookie, base+"/archive", &review)
	masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 303)
	for _, path := range []string{contact, location, old, newContact} {
		var child authorization.CustomerChild
		masterJSON(t, web, cookie, path, &child)
		if child.DeletedAt == nil || child.IsPrimary {
			t.Fatalf("cascade did not archive/clear %s", path)
		}
	}
}

func TestCustomerCascadeAuditReportsOnlyActualPrimaryEffectsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Primary facts").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	primary := masterPost(t, web, cookie, base+"/contacts", url.Values{"first_name": {"Primary"}, "is_primary": {"true"}}, 303)
	secondary := masterPost(t, web, cookie, base+"/contacts", url.Values{"first_name": {"Secondary"}}, 303)
	old := masterPost(t, web, cookie, base+"/contacts", url.Values{"first_name": {"Historical"}}, 303)
	masterPost(t, web, cookie, old+"/delete", nil, 303)
	var oldChild authorization.CustomerChild
	masterJSON(t, web, cookie, old, &oldChild)
	var priorAudit string
	historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id)::text,'[]') FROM authorization_audit a WHERE target_type='contact' AND target_id=$1`, oldChild.ID).Scan(&priorAudit))
	var review authorization.CustomerArchivePreview
	masterJSON(t, web, cookie, base+"/archive", &review)
	before := historyDurableRows(t, pool, []string{"customers", "customer_contacts", "authorization_audit", "activity_logs"})
	_, err := pool.Exec(ctx, `CREATE FUNCTION fail_cascade_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.action='customer.archive' THEN RAISE EXCEPTION 'injected final cascade audit failure'; END IF; RETURN NEW; END$$; CREATE TRIGGER fail_cascade_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_cascade_audit()`)
	historyCheck(t, err)
	masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 409)
	historyEqual(t, "final audit failure rolls back all earlier children and audit", historyDurableRows(t, pool, []string{"customers", "customer_contacts", "authorization_audit", "activity_logs"}), before)
	_, err = pool.Exec(ctx, `DROP TRIGGER fail_cascade_audit ON authorization_audit`)
	historyCheck(t, err)
	masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 303)
	for _, path := range []string{primary, secondary} {
		var child authorization.CustomerChild
		masterJSON(t, web, cookie, path, &child)
		var cleared, wasPrimary, nowPrimary bool
		historyCheck(t, pool.QueryRow(ctx, `SELECT (detail->>'primary_cleared')::boolean,(detail->'before'->>'is_primary')::boolean,(detail->'after'->>'is_primary')::boolean FROM authorization_audit WHERE action='contact.archive' AND target_id=$1`, child.ID).Scan(&cleared, &wasPrimary, &nowPrimary))
		if cleared != (path == primary) || wasPrimary != (path == primary) || nowPrimary {
			t.Fatalf("invented primary effect for %s: cleared=%v before=%v after=%v", path, cleared, wasPrimary, nowPrimary)
		}
	}
	var afterAudit string
	historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id)::text,'[]') FROM authorization_audit a WHERE target_type='contact' AND target_id=$1`, oldChild.ID).Scan(&afterAudit))
	if afterAudit != priorAudit {
		t.Fatal("previously archived child was relabelled as cascade effect")
	}
}

func TestCustomerCascadeRechecksIndependentChildArchiveGrantsHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent grants").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	masterPost(t, web, adminCookie, base+"/contacts", url.Values{"first_name": {"Included person"}}, 303)
	masterPost(t, web, adminCookie, base+"/locations", url.Values{"title": {"Included place"}}, 303)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	client.Asset.Create().SetCompanyID(company).SetOwnershipClass("customer_owned").SetCustomerID(c.ID).SetAssetTypeID(typ.ID).SetName("Included equipment").SaveX(ctx)
	svc := authorization.New(pool)
	var grants []authorization.Grant
	for _, kind := range []string{"customer", "contact", "location", "asset"} {
		for _, action := range []string{"view", "archive"} {
			grants = append(grants, authorization.Grant{Permission: kind + "." + action, Scope: authorization.All})
		}
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Cascade operator", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("cascade@test").SetName("Cascade operator").SetPasswordHash("hash").SetRole("admin").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	var review authorization.CustomerArchivePreview
	masterJSON(t, web, cookie, base+"/archive", &review)
	for _, kind := range []string{"contact", "location", "asset"} {
		t.Run(kind, func(t *testing.T) {
			_, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, kind+".archive")
			historyCheck(t, err)
			before := historyDurableRows(t, pool, []string{"customers", "customer_contacts", "locations", "assets", "authorization_audit", "activity_logs"})
			body := requestBody(t, web, cookie, "GET", base+"/archive", 200)
			assertContains(t, body, kind+".archive")
			assertNotContains(t, body, "Confirm archive")
			masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 403)
			historyEqual(t, "missing independent grant cannot partially archive", historyDurableRows(t, pool, []string{"customers", "customer_contacts", "locations", "assets", "authorization_audit", "activity_logs"}), before)
			_, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,$3)`, role, kind+".archive", authorization.All)
			historyCheck(t, err)
		})
	}
	masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 303)
}
