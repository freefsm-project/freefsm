package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"net/url"
	"testing"
)

func TestLeaseAdjustmentApplicationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var invoice, asset int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice))
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
	path := fmt.Sprintf("/leases/%d/billing/adjustments?invoice_id=%d", lease, invoice)
	body := requestBody(t, web, cookie, "GET", path, 200)
	values := url.Values{"invoice_id": {fmt.Sprint(invoice)}, "asset_id": {fmt.Sprint(asset)}, "kind": {"abatement"}, "amount_cents": {"1"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-01-02"}, "reason": {"Explicit negotiated outage allowance"}, "operation_key": {jobExecutionHidden(t, body, "operation_key")}, "invoice_revision": {jobExecutionHidden(t, body, "invoice_revision")}}
	preserved := historyDurableRows(t, pool, []string{"lease_invoice_generations", "lease_billing_obligations", "lease_asset_dependencies", "lease_finalizations"})
	fieldPost(t, web, cookie, path, values, 303)
	fieldPost(t, web, cookie, path, values, 303)
	var count int
	var total string
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_manual_adjustments WHERE lease_id=$1`, lease).Scan(&count))
	historyEqual(t, "one application after lost response replay", count, 1)
	historyCheck(t, pool.QueryRow(ctx, `SELECT financial::jsonb->'result'->>'total_cents' FROM invoices WHERE id=$1`, invoice).Scan(&total))
	historyEqual(t, "explicit one cent non-taxable allowance", total, "105")
	historyEqual(t, "generation and coverage unchanged", historyDurableRows(t, pool, []string{"lease_invoice_generations", "lease_billing_obligations", "lease_asset_dependencies", "lease_finalizations"}), preserved)
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments)+(SELECT count(*) FROM customer_credits)`).Scan(&count))
	historyEqual(t, "no fabricated cash or Credit", count, 0)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Explicit negotiated outage allowance")
	assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d/billing", lease), 200), "Manual abatements, fees and damages")
	t.Run("invalid amounts and ordered dates retain proposal without effects", func(t *testing.T) {
		before := historyDurableRows(t, pool, []string{"invoices", "lease_manual_adjustments", "authorization_audit"})
		for _, bad := range []struct{ field, value string }{{"amount_cents", "-1"}, {"amount_cents", "1.5"}, {"amount_cents", "100000"}, {"reason", " "}, {"ends_on", "2023-01-01"}} {
			v := url.Values{}
			for k, x := range values {
				v[k] = append([]string(nil), x...)
			}
			current := requestBody(t, web, cookie, "GET", path, 200)
			v.Set("operation_key", jobExecutionHidden(t, current, "operation_key"))
			v.Set("invoice_revision", jobExecutionHidden(t, current, "invoice_revision"))
			v.Set(bad.field, bad.value)
			fieldPost(t, web, cookie, path, v, 409)
			historyEqual(t, "invalid proposal has no partial effects", historyDurableRows(t, pool, []string{"invoices", "lease_manual_adjustments", "authorization_audit"}), before)
		}
	})
	t.Run("independent authority and base nondisclosure", func(t *testing.T) {
		svc := authorization.New(pool)
		actor := authorization.Actor{UserID: admin}
		role, err := svc.SaveRole(ctx, actor, 0, "Base adjustment reader", []authorization.Grant{{Permission: "lease.view", Scope: authorization.All}})
		historyCheck(t, err)
		user := client.User.Create().SetCompanyID(company).SetName("Adjustment denied").SetEmail("adjustment72@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
		denied := sessionCookie(t, ctx, sessions, user.ID)
		requestBody(t, web, denied, "GET", path, 404)
		fieldPost(t, web, denied, path, values, 403)
		assertNotContains(t, requestBody(t, web, denied, "GET", fmt.Sprintf("/leases/%d/history", lease), 200), "Explicit negotiated outage allowance")
		role, err = svc.SaveRole(ctx, actor, 0, "Billing without Invoice edit", []authorization.Grant{{Permission: "lease.billing.view", Scope: authorization.All}, {Permission: "lease.billing.manage", Scope: authorization.All}})
		historyCheck(t, err)
		historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
		requestBody(t, web, denied, "GET", path, 404)
		fieldPost(t, web, denied, path, values, 404)
		grants := []authorization.Grant{{Permission: "lease.billing.view", Scope: authorization.All}, {Permission: "lease.billing.manage", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.edit", Scope: authorization.All}, {Permission: "pricing.view", Scope: authorization.All}}
		role, err = svc.SaveRole(ctx, actor, 0, "Independent adjustment writer", grants)
		historyCheck(t, err)
		historyCheck(t, svc.AssignUser(ctx, actor, user.ID, role))
		for _, permission := range []string{"invoice.edit", "lease.billing.manage"} {
			current := requestBody(t, web, denied, "GET", path, 200)
			v := url.Values{}
			for k, x := range values {
				v[k] = append([]string(nil), x...)
			}
			v.Set("operation_key", jobExecutionHidden(t, current, "operation_key"))
			v.Set("invoice_revision", jobExecutionHidden(t, current, "invoice_revision"))
			queuedChildPost(t, pool, web, denied, path, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, []any{role, permission}, 403, v)
			_, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,'A')`, role, permission)
			historyCheck(t, err)
		}
		api := actor
		api.Channel = "api"
		if _, err = svc.ReviewLeaseAdjustments(ctx, api, lease, invoice); err == nil {
			t.Fatal("API exposed Lease adjustments")
		}
		if err = svc.ApplyLeaseAdjustment(ctx, api, lease, authorization.LeaseAdjustmentInput{}); err == nil {
			t.Fatal("API accepted Lease adjustment")
		}
	})
	t.Run("atomic audit failure and retry", func(t *testing.T) {
		current := requestBody(t, web, cookie, "GET", path, 200)
		values.Set("invoice_revision", jobExecutionHidden(t, current, "invoice_revision"))
		values.Set("operation_key", jobExecutionHidden(t, current, "operation_key"))
		values.Set("kind", "fee")
		values.Set("amount_cents", "15")
		before := historyDurableRows(t, pool, []string{"invoices", "lease_manual_adjustments", "authorization_audit"})
		_, err := pool.Exec(ctx, `CREATE FUNCTION reject_adjustment_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.billing.adjust' THEN RAISE EXCEPTION 'private adjustment failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_adjustment_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_adjustment_audit()`)
		historyCheck(t, err)
		response := fieldPost(t, web, cookie, path, values, 409).Body.String()
		assertContains(t, response, "transaction was rolled back")
		assertNotContains(t, response, "private adjustment failure")
		historyEqual(t, "Invoice and application roll back with audit", historyDurableRows(t, pool, []string{"invoices", "lease_manual_adjustments", "authorization_audit"}), before)
		_, err = pool.Exec(ctx, `DROP TRIGGER reject_adjustment_audit ON authorization_audit`)
		historyCheck(t, err)
		fieldPost(t, web, cookie, path, values, 303)
		values.Set("amount_cents", "16")
		fieldPost(t, web, cookie, path, values, 409)
		for _, q := range []string{`UPDATE lease_manual_adjustments SET reason='rewrite'`, `DELETE FROM lease_manual_adjustments`} {
			if _, err = pool.Exec(ctx, q); err == nil {
				t.Fatal("mutable adjustment history")
			}
		}
	})
}
