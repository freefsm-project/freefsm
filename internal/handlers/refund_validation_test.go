package handlers

import (
	"fmt"
	"net/url"
	"testing"
	"time"
)

func (f *creditMatrix) refund(amount string) creditCommand {
	return creditCommand{fmt.Sprintf("/customers/%d/credit/refunds", f.customer), url.Values{"idempotency_key": {newOperationKey()}, "amount": {amount}, "method": {"cash"}, "effective_date": {"2026-01-02"}, "reason": {"External refund recorded"}}}
}

func TestRefundValidationHTTP(t *testing.T) {
	f := newCreditMatrix(t)
	for _, tc := range []struct {
		name, key, value string
		status           int
	}{
		{"zero", "amount", "0", 400}, {"negative", "amount", "-1", 400}, {"precision", "amount", "1.001", 400}, {"overflow", "amount", "92233720368547758.08", 400}, {"aggregate", "amount", "100.01", 422},
		{"method", "method", "wire", 422}, {"reason", "reason", "  ", 422}, {"missing-date", "effective_date", "", 422}, {"invalid-date", "effective_date", "2026-02-30", 422}, {"before-source", "effective_date", "2026-01-01", 422}, {"future", "effective_date", "2999-01-01", 422},
	} {
		t.Run(tc.name, func(t *testing.T) { v := f.refund("1.00"); v.values.Set(tc.key, tc.value); f.post(t, v, tc.status) })
	}
	for _, field := range []string{"amount", "method", "effective_date", "reason", "idempotency_key"} {
		v := f.refund("1.00")
		v.values.Add(field, "changed-intent")
		f.post(t, v, 400)
	}
	for _, zone := range []string{"Pacific/Kiritimati", "America/Los_Angeles"} {
		_, err := f.pool.Exec(f.ctx, `UPDATE company_settings SET timezone=$1`, zone)
		historyCheck(t, err)
		loc, err := time.LoadLocation(zone)
		historyCheck(t, err)
		today := time.Now().In(loc)
		for _, method := range []string{"cash", "check", "credit_card", "transfer", "other"} {
			v := f.refund("0.01")
			v.values.Set("method", method)
			v.values.Set("effective_date", today.AddDate(0, 0, 1).Format("2006-01-02"))
			f.post(t, v, 422)
			v.values.Set("effective_date", today.Format("2006-01-02"))
			f.post(t, v, 303)
			f.post(t, v, 303)
		}
	}
	f.balances(t, 10000, 9990, 10000)
}

func TestRefundFIFOInstalledTiesAndExactRestorationHTTP(t *testing.T) {
	f := newCreditMatrix(t)
	// A valid installed fixture with tied source/entry dates and deliberately
	// reverse insertion order. Stable source identity is the final FIFO tie-break.
	for _, i := range []int{2, 1} {
		payment := fmt.Sprintf("53000000-0000-4000-8000-%012d", i)
		source := fmt.Sprintf("53000000-0000-4000-9000-%012d", i)
		amount := 2000
		if i == 1 {
			amount = 3000
		}
		_, err := f.pool.Exec(f.ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,amount_cents,method,received_date,actor_id,created_at) VALUES($1,$2,$3,$4,'cash','2026-01-01',$5,'2026-01-01T12:00Z');`, payment, f.actor.CompanyID, f.customer, amount, f.actor.ID)
		historyCheck(t, err)
		_, err = f.pool.Exec(f.ctx, `INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date,created_at) VALUES($1,$2,$3,$4,$5,'2026-01-01','2026-01-01T12:00Z')`, source, f.actor.CompanyID, f.customer, payment, amount)
		historyCheck(t, err)
	}
	v := f.refund("40.00")
	f.post(t, v, 303)
	var id, portions string
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT f.id,(SELECT string_agg(a.credit_id::text||':'||a.amount_cents::text,',' ORDER BY a.credit_id) FROM credit_refund_allocations a WHERE a.refund_id=f.id) FROM credit_refunds f`).Scan(&id, &portions))
	want := "53000000-0000-4000-9000-000000000001:3000,53000000-0000-4000-9000-000000000002:1000"
	if portions != want {
		t.Fatalf("tied FIFO portions=%s", portions)
	}
	f.post(t, creditCommand{fmt.Sprintf("/customers/%d/payments", f.customer), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"5.00"}, "method": {"cash"}, "date": {"2025-01-01"}}}, 303)
	f.post(t, creditCommand{v.path + "/" + id + "/reverse", url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"Restore original portions"}}}, 303)
	f.balances(t, 10000, 15500, 15500)
	page := documentTransactionRequest(t, f.web, f.cookie, "GET", fmt.Sprintf("/customers/%d/refunds", f.customer), nil, 200)
	assertContains(t, page.Body.String(), "53000000-0000-4000-9000-000000000001")
	assertContains(t, page.Body.String(), "53000000-0000-4000-9000-000000000002")
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT string_agg(credit_id::text||':'||amount_cents::text,',' ORDER BY credit_id) FROM credit_refund_allocations WHERE refund_id=$1`, id).Scan(&portions))
	if portions != want {
		t.Fatal("reversal rewrote original source portions")
	}
	// A new refund now consumes the genuinely older, newly recorded source first.
	f.post(t, f.refund("6.00"), 303)
	var older int64
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT sum(a.amount_cents) FROM credit_refund_allocations a JOIN customer_credits c ON c.id=a.credit_id WHERE c.source_date='2025-01-01'`).Scan(&older))
	if older != 500 {
		t.Fatalf("new oldest portion=%d", older)
	}
}
