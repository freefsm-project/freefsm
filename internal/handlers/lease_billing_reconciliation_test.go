package handlers

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/assettype"
)

func TestLeaseBillingReconcilesPendingAndIsolatesPeriods(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	bad, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	client.AssetType.UpdateOne(client.AssetType.Query().Where(assettype.NameEQ("Finalized equipment")).OnlyX(ctx)).SetName("First agreement equipment").SaveX(ctx)
	good, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	before := historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations", "lease_billing_obligations"})
	var customer, pending int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id FROM leases WHERE id=$1`, bad).Scan(&customer))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly'`, bad).Scan(&pending))
	exempt := true
	svc := authorization.New(pool)
	_, err = svc.SaveCustomer(ctx, authorization.Actor{UserID: admin}, customer, authorization.CustomerInput{TaxExempt: &exempt})
	historyCheck(t, err)
	historyEqual(t, "source change does not rewrite original facts", historyDurableRows(t, pool, []string{"invoices", "lease_invoice_generations", "lease_billing_obligations"}), before)
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-04-01'::date $$; CREATE FUNCTION isolated_billing_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.invoice.generate' AND (NEW.detail->>'obligation_id')::bigint=`+strconv.FormatInt(pending, 10)+` THEN RAISE EXCEPTION 'one period storage fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER isolated_billing_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION isolated_billing_fault()`)
	historyCheck(t, err)
	if err = svc.GenerateLeaseDrafts(ctx); err == nil {
		t.Fatal("period fault reported success")
	}
	var badCount, goodCount int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1`, bad).Scan(&badCount))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1`, good).Scan(&goodCount))
	historyEqual(t, "one failed period cannot block other periods", badCount, 4)
	historyEqual(t, "one failed Lease cannot block another", goodCount, 5)
	body := requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d/billing/periods/%d", bad, pending), 200)
	assertContains(t, body, "Pending source reconciliation history")
	assertContains(t, body, "Original approval: 5250 cents")
	assertContains(t, body, "approved total 5000 cents")
	assertContains(t, requestBody(t, web, cookie, "GET", fmt.Sprintf("/leases/%d/billing", bad), 200), "Periods requiring retry")
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2028-01-01'::date $$`)
	historyCheck(t, err)
	for pass := 0; pass < 3; pass++ {
		if err = svc.GenerateLeaseDrafts(ctx); err == nil {
			t.Fatal("persistent period fault hidden")
		}
	}
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1`, bad).Scan(&badCount))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_invoice_generations g JOIN lease_billing_obligations o ON o.id=g.obligation_id WHERE o.lease_id=$1`, good).Scan(&goodCount))
	historyEqual(t, "bounded catch-up leaves only the failed period", badCount, 48)
	historyEqual(t, "every eligible month survives long downtime", goodCount, 49)
	_, err = pool.Exec(ctx, `DROP TRIGGER isolated_billing_fault ON authorization_audit`)
	historyCheck(t, err)
	historyCheck(t, svc.GenerateLeaseDrafts(ctx))
	var preserved bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT o.financial::jsonb->'result'->>'total_cents'='5250' AND i.financial::jsonb->'result'->>'total_cents'='5000' FROM lease_billing_obligations o JOIN lease_invoice_generations g ON g.obligation_id=o.id JOIN invoices i ON i.id=g.invoice_id WHERE o.id=$1`, pending).Scan(&preserved))
	historyEqual(t, "same pending ID reconciles exemption while preserving original approval", preserved, true)
	var revisions int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_obligation_revisions WHERE obligation_id=$1`, pending).Scan(&revisions))
	historyEqual(t, "retry reuses the reconciled source revision", revisions, 1)
}

func TestLeaseBillingManualReconciliationRetainsFailedRevision(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	lease, _ := populateFinalizedLease(t, pool, client, web, cookie, company)
	var pending, customer int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE kind='monthly'`).Scan(&pending))
	historyCheck(t, pool.QueryRow(ctx, `SELECT customer_id FROM leases WHERE id=$1`, lease).Scan(&customer))
	exempt := true
	_, err = authorization.New(pool).SaveCustomer(ctx, authorization.Actor{UserID: admin}, customer, authorization.CustomerInput{TaxExempt: &exempt})
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-02'::date $$; CREATE FUNCTION manual_reconciliation_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.invoice.generate' THEN RAISE EXCEPTION 'manual generation fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER manual_reconciliation_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION manual_reconciliation_fault()`)
	historyCheck(t, err)
	path := fmt.Sprintf("/leases/%d/billing/periods/%d", lease, pending)
	body := requestBody(t, web, cookie, "GET", path, 200)
	assertContains(t, body, "approved total 5000 cents")
	values := url.Values{"review_hash": {jobExecutionHidden(t, body, "review_hash")}, "operation_key": {jobExecutionHidden(t, body, "operation_key")}}
	var revisions int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_obligation_revisions`).Scan(&revisions))
	historyEqual(t, "review is not a write", revisions, 0)
	fieldPost(t, web, cookie, path+"/retry", values, 409)
	fieldPost(t, web, cookie, path+"/retry", values, 409)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_obligation_revisions`).Scan(&revisions))
	historyEqual(t, "failed manual attempts retain one source revision", revisions, 1)
	var invoices int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&invoices))
	historyEqual(t, "failed manual attempt retains no partial Invoice", invoices, 1)
	_, err = pool.Exec(ctx, `DROP TRIGGER manual_reconciliation_fault ON authorization_audit`)
	historyCheck(t, err)
	fieldPost(t, web, cookie, path+"/retry", values, 303)
	fieldPost(t, web, cookie, path+"/retry", values, 303)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM lease_billing_obligation_revisions`).Scan(&revisions))
	historyEqual(t, "successful replay keeps the same source revision", revisions, 1)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&invoices))
	historyEqual(t, "one complete manual generation", invoices, 2)
}
