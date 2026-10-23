package handlers

import (
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestCreditApplicationRemainingEligibilityHTTP(t *testing.T) {
	for _, scenario := range []string{"zero", "negative", "precision", "missing-source", "multiple-sources", "multiple-amounts", "void", "zero-total", "paid", "archived-invoice", "hidden-invoice", "reversed-source", "refund-not-source"} {
		t.Run(scenario, func(t *testing.T) {
			f := newCreditMatrix(t)
			cmd := f.apply(f.invoice, "10.00")
			want := 422
			switch scenario {
			case "zero":
				cmd.values.Set("amount", "0")
				want = 400
			case "negative":
				cmd.values.Set("amount", "-1.00")
				want = 400
			case "precision":
				cmd.values.Set("amount", "0.001")
				want = 400
			case "missing-source":
				cmd.values.Del("credit_id")
				want = 400
			case "multiple-sources":
				cmd.values.Add("credit_id", newOperationKey())
				want = 400
			case "multiple-amounts":
				cmd.values.Add("amount", "1.00")
				want = 400
			case "void", "archived-invoice":
				var status int64
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM statuses WHERE category_key='invoice:void' LIMIT 1`).Scan(&status))
				historyCheck(t, statusflow.New(f.pool).TransitionInvoice(f.ctx, statusflow.Actor{ID: f.actor.ID, CompanyID: f.actor.CompanyID}, f.invoice, status))
				want = 409
				if scenario == "archived-invoice" {
					f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/delete", f.invoice), nil}, 303)
				}
			case "zero-total":
				f.post(t, f.edit(t, "0"), 303)
			case "paid":
				f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/payments-with-credit", f.invoice), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}}, 303)
			case "hidden-invoice":
				f.post(t, creditCommand{"/estimates", url.Values{"customer_id": {fmt.Sprint(f.customer)}, "title": {"Reverted source"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}}, 303)
				var estimate, hidden int64
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT max(id) FROM estimates`).Scan(&estimate))
				f.post(t, creditCommand{fmt.Sprintf("/estimates/%d/convert", estimate), url.Values{"idempotency_key": {newOperationKey()}}}, 303)
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT max(id) FROM invoices`).Scan(&hidden))
				f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/revert-to-estimate", hidden), url.Values{"idempotency_key": {newOperationKey()}}}, 303)
				cmd.path = fmt.Sprintf("/invoices/%d/credit-applications", hidden)
				want = 404
			case "reversed-source":
				f.post(t, creditCommand{fmt.Sprintf("/customers/%d/payments/%s/reverse", f.customer, f.payment), url.Values{"idempotency_key": {newOperationKey()}, "date": {"2026-01-02"}, "reason": {"external_return"}, "notes": {"source returned"}}}, 303)
				want = 404
			case "refund-not-source":
				f.post(t, creditCommand{fmt.Sprintf("/customers/%d/credit/refunds", f.customer), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"1.00"}, "method": {"cash"}, "effective_date": {"2026-01-02"}, "reason": {"external refund"}}}, 303)
				var refund string
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_refunds`).Scan(&refund))
				cmd.values.Set("credit_id", refund)
				want = 404
			}
			before := creditEligibilityRows(t, f)
			f.post(t, cmd, want)
			historyEqual(t, "ineligible Application leaves original facts, balances, receipts and audit unchanged", creditEligibilityRows(t, f), before)
		})
	}
}

func creditEligibilityRows(t *testing.T, f *creditMatrix) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"invoice_payments", "customer_credits", "credit_applications", "settlement_reversals", "settlement_idempotency", "operation_admissions", "invoices", "authorization_audit"} {
		var raw string
		historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM `+table+` r`).Scan(&raw))
		out[table] = raw
	}
	return out
}

func TestCreditApplicationMultipleSourcesExactRestorationHTTP(t *testing.T) {
	f := newCreditMatrix(t)
	f.post(t, creditCommand{fmt.Sprintf("/customers/%d/payments", f.customer), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"80.00"}, "method": {"transfer"}, "date": {"2026-01-02"}}}, 303)
	var second string
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM customer_credits WHERE id<>$1`, f.source).Scan(&second))
	a := f.apply(f.invoice, "40.00")
	b := f.apply(f.invoice, "60.00")
	b.values.Set("credit_id", second)
	f.post(t, a, 303)
	f.post(t, b, 303)
	f.balances(t, 0, 8000, 18000)
	var originalA, originalB string
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_applications WHERE credit_id=$1`, f.source).Scan(&originalA))
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_applications WHERE credit_id=$1`, second).Scan(&originalB))
	f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/credit-applications/%s/reverse", f.invoice, originalA), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"only first source was wrong"}}}, 303)
	f.balances(t, 4000, 12000, 18000)
	// The restored40 belongs solely to source A. Source B still has only20;
	// attempting40 from B must not consume A or shrink the requested amount.
	b.values.Set("idempotency_key", newOperationKey())
	b.values.Set("amount", "40.00")
	f.post(t, b, 422)
	a.values.Set("idempotency_key", newOperationKey())
	f.post(t, a, 303)
	f.balances(t, 0, 8000, 18000)
	var activeB, originals int
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM credit_applications a WHERE a.id=$1 AND NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_id=a.id)),(SELECT count(*) FROM credit_applications WHERE id IN ($1,$2))`, originalB, originalA).Scan(&activeB, &originals))
	if activeB != 1 || originals != 2 {
		t.Fatal("reversal changed another source Application or rewrote original history")
	}
}
