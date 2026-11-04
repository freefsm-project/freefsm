package handlers

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestLeaseAdjustmentArchivedHistoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var invoice, asset int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE lease_id=$1`, lease).Scan(&invoice))
	historyCheck(t, pool.QueryRow(ctx, `SELECT asset_id FROM lease_asset_dependencies WHERE lease_id=$1`, lease).Scan(&asset))
	path := fmt.Sprintf("/leases/%d/billing/adjustments", lease)
	body := requestBody(t, web, cookie, "GET", fmt.Sprintf("%s?invoice_id=%d", path, invoice), 200)
	values := url.Values{"invoice_id": {fmt.Sprint(invoice)}, "asset_id": {fmt.Sprint(asset)}, "kind": {"abatement"}, "amount_cents": {"1"}, "starts_on": {"2024-01-01"}, "ends_on": {"2024-01-02"}, "reason": {"Retained archived outage allowance"}, "operation_key": {jobExecutionHidden(t, body, "operation_key")}, "invoice_revision": {jobExecutionHidden(t, body, "invoice_revision")}}
	fieldPost(t, web, cookie, path, values, 303)
	var beforeValue, afterValue string
	historyCheck(t, pool.QueryRow(ctx, `SELECT before_value::text,after_value::text FROM lease_manual_adjustments WHERE lease_id=$1`, lease).Scan(&beforeValue, &afterValue))
	retained := historyDurableRows(t, pool, []string{"lease_manual_adjustments"})
	flow := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}
	historyCheck(t, flow.FinalizeInvoice(ctx, actor, invoice))
	var void int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void' AND is_category_default`, company).Scan(&void))
	historyCheck(t, flow.TransitionInvoice(ctx, actor, invoice, void))
	fieldPost(t, web, cookie, fmt.Sprintf("/invoices/%d/delete", invoice), url.Values{}, 303)
	assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "Retained archived outage allowance")
	svc := authorization.New(pool)
	adminActor := authorization.Actor{UserID: admin}
	user := client.User.Create().SetCompanyID(company).SetName("Archived adjustment reader").SetEmail("archive-adjustment@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	reader := sessionCookie(t, ctx, sessions, user.ID)
	permissions := []string{"lease.billing.view", "invoice.view", "invoice.archived.view", "pricing.view"}
	for _, missing := range append([]string{"", "unassigned pricing"}, permissions...) {
		t.Run("missing_"+missing, func(t *testing.T) {
			var grants []authorization.Grant
			for _, permission := range permissions {
				if permission != missing || missing == "invoice.view" && permission == "invoice.view" {
					scope := authorization.All
					if missing == "unassigned pricing" && permission == "pricing.view" {
						scope = authorization.Assigned
					}
					grants = append(grants, authorization.Grant{Permission: permission, Scope: scope})
				}
			}
			role, err := svc.SaveRole(ctx, adminActor, 0, "Archived reader missing "+missing, grants)
			historyCheck(t, err)
			historyCheck(t, svc.AssignUser(ctx, adminActor, user.ID, role))
			if missing == "invoice.view" {
				// Role validation prevents this combination; emulate later grant
				// loss to prove the reader independently rechecks parent View.
				_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='invoice.view'`, role)
				historyCheck(t, err)
			}
			status := 200
			if missing == "lease.billing.view" {
				status = 404
			}
			body := requestBody(t, web, reader, "GET", path, status)
			if missing == "" {
				assertContains(t, body, "Retained archived outage allowance")
				assertContains(t, body, "Before")
				assertContains(t, body, "After")
				assertContains(t, html.UnescapeString(body), beforeValue)
				assertContains(t, html.UnescapeString(body), afterValue)
			} else {
				assertNotContains(t, body, "Retained archived outage allowance")
			}
		})
	}
	before := historyDurableRows(t, pool, []string{"invoices", "lease_manual_adjustments", "authorization_audit"})
	values.Set("operation_key", "archived-adjustment-rejected-0001")
	fieldPost(t, web, cookie, path, values, 403)
	historyEqual(t, "archived application rejected atomically", historyDurableRows(t, pool, []string{"invoices", "lease_manual_adjustments", "authorization_audit"}), before)
	historyEqual(t, "archive and reads retain immutable before/after", historyDurableRows(t, pool, []string{"lease_manual_adjustments"}), retained)
	for _, query := range []string{`UPDATE lease_manual_adjustments SET reason='rewrite'`, `DELETE FROM lease_manual_adjustments`} {
		if _, err := pool.Exec(ctx, query); err == nil {
			t.Fatal("archived adjustment history is mutable")
		}
	}
}
