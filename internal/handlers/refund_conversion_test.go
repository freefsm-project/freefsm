package handlers

import (
	"fmt"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"net/url"
	"testing"
)

func TestRefundConversionRequiresCompleteUnwindHTTP(t *testing.T) {
	for _, zero := range []bool{false, true} {
		t.Run(fmt.Sprint(zero), func(t *testing.T) {
			f := newCreditMatrix(t)
			amount := "100"
			if zero {
				amount = "0"
			}
			f.post(t, creditCommand{"/estimates", url.Values{"customer_id": {fmt.Sprint(f.customer)}, "title": {"Conversion financial provenance"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":` + amount + `}]`}, "tax_rate": {"0"}}}, 303)
			var estimate, invoice int64
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM estimates`).Scan(&estimate))
			f.post(t, creditCommand{fmt.Sprintf("/estimates/%d/convert", estimate), url.Values{"idempotency_key": {newOperationKey()}}}, 303)
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, estimate).Scan(&invoice))
			historyCheck(t, statusflow.New(f.pool).FinalizeInvoice(f.ctx, statusflow.Actor{ID: f.actor.ID, CompanyID: f.actor.CompanyID}, invoice))
			if !zero {
				f.post(t, f.apply(invoice, "25.00"), 303)
				f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/revert-to-estimate", invoice), url.Values{"idempotency_key": {newOperationKey()}}}, 422)
				var incoming string
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_applications WHERE invoice_id=$1`, invoice).Scan(&incoming))
				f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/credit-applications/%s/reverse", invoice, incoming), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"Unwind incoming Application"}}}, 303)
			}
			var payment string
			if zero {
				// Installed pre-office-admission writer allowed a receipt entirely funded
				// into Credit on a zero Invoice. Preserve that real historical shape.
				p, err := settlement.New(f.pool).RecordPayment(f.ctx, f.actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, AmountCents: 5000, Method: settlement.Cash, ReceivedDate: "2026-01-03"})
				historyCheck(t, err)
				payment = p.ID.String()
			} else {
				f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/payments-with-credit", invoice), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"150.00"}, "method": {"cash"}, "date": {"2026-01-03"}}}, 303)
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM invoice_payments WHERE invoice_id=$1`, invoice).Scan(&payment))
			}
			var source string
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM customer_credits WHERE source_payment_id=$1`, payment).Scan(&source))
			// Exhaust the unrelated older source so FIFO consumes the conversion's exact
			// source, half by Application and half by refund. Net available is zero.
			f.post(t, f.refund("100.00"), 303)
			app := f.apply(f.invoice, "25.00")
			app.values.Set("credit_id", source)
			app.values.Set("effective_date", "2026-01-03")
			f.post(t, app, 303)
			refund := f.refund("25.00")
			refund.values.Set("effective_date", "2026-01-03")
			f.post(t, refund, 303)
			var application, refundID string
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_applications WHERE invoice_id=$1`, f.invoice).Scan(&application))
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT result_id FROM settlement_idempotency WHERE operation='refund_credit' AND idempotency_key=$1`, refund.values.Get("idempotency_key")).Scan(&refundID))
			revert := creditCommand{fmt.Sprintf("/invoices/%d/revert-to-estimate", invoice), url.Values{"idempotency_key": {newOperationKey()}}}
			reversePayment := creditCommand{fmt.Sprintf("/invoices/%d/payments/%s/reverse", invoice, payment), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-03"}, "reason": {"error"}}}
			f.post(t, revert, 422)
			f.post(t, reversePayment, 409)
			f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/credit-applications/%s/reverse", f.invoice, application), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-03"}, "reason": {"Explicit unwind"}}}, 303)
			f.post(t, revert, 422)
			f.post(t, reversePayment, 409)
			f.post(t, creditCommand{fmt.Sprintf("/customers/%d/credit/refunds/%s/reverse", f.customer, refundID), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-03"}, "reason": {"Explicit unwind"}}}, 303)
			f.post(t, revert, 422) // Restored origin Credit is still an active effect.
			f.post(t, reversePayment, 303)
			f.post(t, revert, 303)
			// The installed web guard rejects ordinary access to the resulting
			// hidden tombstone, including another POST; financial history remains.
			f.post(t, revert, 403)
			var intact bool
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT p.invoice_id=$2 AND c.original_amount_cents=5000 AND (SELECT count(*) FROM credit_refund_allocations WHERE refund_id=$3)=1 FROM invoice_payments p JOIN customer_credits c ON c.source_payment_id=p.id WHERE p.id=$1`, payment, invoice, refundID).Scan(&intact))
			if !intact {
				t.Fatal("unwind rewrote origin history")
			}
		})
	}
}
