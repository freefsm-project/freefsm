package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

// This seam starts with real office HTTP finalization and observes ordinary
// Invoices plus the retained pending identity after a simulated outage.
func TestLeaseBillingWorkerCatchupHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	clock := func(date string) {
		t.Helper()
		_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '`+date+`'::date $$`)
		historyCheck(t, err)
	}
	clock("2024-01-01")
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Worker Customer").SaveX(ctx)
	l := client.Location.Create().SetCompanyID(company).SetObjectType("customer").SetObjectID(c.ID).SetTitle("Premises").SaveX(ctx)
	typ := client.AssetType.Create().SetCompanyID(company).SetName("Equipment").SaveX(ctx)
	a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Covered").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/leases", url.Values{"customer_id": {fmt.Sprint(c.ID)}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM leases`).Scan(&id))
	path := fmt.Sprintf("/leases/%d", id)
	fieldPost(t, web, cookie, path, url.Values{"revision": {"0"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-12-31"}, "premises_id": {fmt.Sprint(l.ID)}, "asset_ids": {fmt.Sprint(a.ID)}}, 303)
	amount, rent := int64(100), int64(5000)
	svc := authorization.New(pool)
	historyCheck(t, svc.SaveDraftLeaseBilling(ctx, authorization.Actor{UserID: admin}, id, 0, "2024-01-01", authorization.DraftLeaseTerms{AssetID: a.ID, InitialCents: &amount, RentCents: &rent, TaxRate: "5"}, 0))
	grants := []authorization.Grant{}
	for _, key := range []string{"lease.view", "lease.manage", "lease.billing.view", "lease.billing.manage"} {
		grants = append(grants, authorization.Grant{Permission: key, Scope: authorization.All})
	}
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Billing approver", grants)
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Approver").SetEmail("worker67@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	historyCheck(t, svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie = sessionCookie(t, ctx, sessions, u.ID)
	fieldPost(t, web, cookie, path+"/finalize", url.Values{"revision": {"1"}, "billing_revision": {"1"}, "managed_billing_start": {"2024-01-01"}}, 303)
	var pending int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE kind='monthly'`).Scan(&pending))
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var early int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&early))
	historyEqual(t, "31 days before due remains ungenerated", early, 1)
	clock("2024-04-01")
	_, err = pool.Exec(ctx, `CREATE FUNCTION reject_worker_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.actor_kind='lease-billing-worker' THEN RAISE EXCEPTION 'injected worker audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_worker_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_worker_audit()`)
	historyCheck(t, err)
	if err := svc.GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("worker hid failed generation")
	}
	var failedCount int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&failedCount))
	historyEqual(t, "failed worker leaves only initial Invoice", failedCount, 1)
	assertContains(t, requestBody(t, web, cookie, "GET", path+"/finalize", 200), "Draft generation failed")
	_, err = pool.Exec(ctx, `DROP TRIGGER reject_worker_audit ON authorization_audit`)
	historyCheck(t, err)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	// February, March, April and May (exactly 30 days away), plus initial.
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations`).Scan(&count))
	historyEqual(t, "all missed periods", count, 5)
	var retained bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lease_invoice_generations WHERE obligation_id=$1)`, pending).Scan(&retained))
	historyEqual(t, "pending identity reused", retained, true)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations`).Scan(&count))
	historyEqual(t, "lost response replay", count, 5)
	var invoice int64
	var correct bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT i.id,s.category_key='invoice:draft' AND i.lease_id=$1 AND i.line_items->0->>'asset_id'=$2 AND i.line_items->0->>'period'='2024-02-01' AND i.line_items->0->>'charge_kind'='monthly' AND i.financial::jsonb->'result'->>'total_cents'='5250' FROM invoices i JOIN statuses s ON s.id=i.status_id JOIN lease_invoice_generations g ON g.invoice_id=i.id WHERE g.obligation_id=$3`, id, fmt.Sprint(a.ID), pending).Scan(&invoice, &correct))
	historyEqual(t, "ordinary Draft totals and provenance", correct, true)
	requestBody(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d", invoice), 403)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE actor_kind='lease-billing-worker' AND actor_id IS NULL AND role_id IS NULL AND action='lease.invoice.generate'`).Scan(&count))
	historyEqual(t, "worker attribution without impersonation", count, 4)
}
