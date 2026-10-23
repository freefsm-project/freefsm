package handlers

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
)

func (f *creditMatrix) reverseRefund(t *testing.T, amount string) creditCommand {
	t.Helper()
	f.post(t, f.refund(amount), 303)
	var id string
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_refunds ORDER BY created_at DESC LIMIT 1`).Scan(&id))
	return creditCommand{fmt.Sprintf("/customers/%d/credit/refunds/%s/reverse", f.customer, id), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"Correct external record"}}}
}

func TestRefundSourceReversalContentionHTTP(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, first := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse_%v/first_%v", reverse, first), func(t *testing.T) {
				f := newCreditMatrix(t)
				primary := f.refund("60.00")
				if reverse {
					primary = f.reverseRefund(t, "60.00")
				}
				source := creditCommand{fmt.Sprintf("/customers/%d/payments/%s/reverse", f.customer, f.payment), url.Values{"idempotency_key": {newOperationKey()}, "date": {"2026-01-02"}, "reason": {"error"}}}
				var p, s *httptest.ResponseRecorder
				if first {
					p, s = f.ordered(t, primary, source)
				} else {
					s, p = f.ordered(t, source, primary)
				}
				wp, ws := 303, 409
				var available, cash int64 = 4000, 10000
				if reverse {
					available = 10000
					if first {
						ws = 303
						available = 0
						cash = 0
					}
				} else if !first {
					wp = 422
					ws = 303
					available = 0
					cash = 0
				}
				if p.Code != wp || s.Code != ws {
					t.Fatalf("refund=%d want%d %s source=%d want%d %s", p.Code, wp, p.Body.String(), s.Code, ws, s.Body.String())
				}
				f.balances(t, 10000, available, cash)
			})
		}
	}
}

func TestRefundInvoiceEditAndArchiveContentionHTTP(t *testing.T) {
	for _, kind := range []string{"refund-edit", "payment-reversal-edit", "refund-reversal-customer-archive", "payment-reversal-invoice-archive"} {
		for _, first := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/first_%v", kind, first), func(t *testing.T) {
				f := newCreditMatrix(t)
				primary := f.refund("60.00")
				var other creditCommand
				switch kind {
				case "refund-edit":
					other = f.edit(t, "50")
				case "refund-reversal-customer-archive":
					f.post(t, f.edit(t, "0"), 303)
					primary = f.reverseRefund(t, "100.00")
					var preview struct{ Snapshot string }
					base := fmt.Sprintf("/customers/%d", f.customer)
					masterJSON(t, f.web, f.cookie, base+"/archive", &preview)
					other = creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}
				default:
					f.post(t, creditCommand{fmt.Sprintf("/invoices/%d/payments-with-credit", f.invoice), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}}, 303)
					var id string
					historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM invoice_payments WHERE invoice_id=$1`, f.invoice).Scan(&id))
					primary = creditCommand{fmt.Sprintf("/invoices/%d/payments/%s/reverse", f.invoice, id), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"failure"}}}
					if kind == "payment-reversal-edit" {
						other = f.edit(t, "50")
					} else {
						other = creditCommand{fmt.Sprintf("/invoices/%d/delete", f.invoice), nil}
					}
				}
				var p, o *httptest.ResponseRecorder
				if first {
					p, o = f.ordered(t, primary, other)
				} else {
					o, p = f.ordered(t, other, primary)
				}
				switch kind {
				case "refund-edit":
					if p.Code != 303 || o.Code != 303 {
						t.Fatalf("refund=%d %s edit=%d %s", p.Code, p.Body.String(), o.Code, o.Body.String())
					}
					f.balances(t, 5000, 4000, 10000)
				case "payment-reversal-edit":
					if p.Code != 303 || (o.Code != 409 && o.Code != 422) {
						t.Fatalf("reversal=%d edit=%d %s", p.Code, o.Code, o.Body.String())
					}
					f.balances(t, 10000, 10000, 10000)
					f.post(t, f.edit(t, "50"), 303)
				default:
					wp, wo := 409, 303
					if first {
						wp, wo = 303, 409
					}
					if p.Code != wp || o.Code != wo {
						t.Fatalf("reversal=%d want%d %s archive=%d want%d %s", p.Code, wp, p.Body.String(), o.Code, wo, o.Body.String())
					}
					if kind == "refund-reversal-customer-archive" {
						available := int64(0)
						if first {
							available = 10000
						}
						f.balances(t, 0, available, 10000)
					} else if first {
						f.balances(t, 10000, 10000, 10000)
					} else {
						f.balances(t, 0, 10000, 20000)
					}
				}
			})
		}
	}
}

func TestRefundConcurrentIdentityHTTP(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		for _, same := range []bool{false, true} {
			t.Run(fmt.Sprintf("reverse_%v/same_%v", reverse, same), func(t *testing.T) {
				f := newCreditMatrix(t)
				a := f.refund("60.00")
				if reverse {
					a = f.reverseRefund(t, "60.00")
				}
				b := creditCommand{a.path, url.Values{}}
				for k, v := range a.values {
					b.values[k] = append([]string(nil), v...)
				}
				if !same {
					b.values.Set("idempotency_key", newOperationKey())
				}
				x, y := f.ordered(t, a, b)
				want := 422
				if reverse {
					want = 409
				}
				if same {
					want = 303
				}
				if x.Code != 303 || y.Code != want {
					t.Fatalf("first=%d second=%d want%d", x.Code, y.Code, want)
				}
				available := int64(4000)
				if reverse {
					available = 10000
				}
				f.balances(t, 10000, available, 10000)
			})
		}
	}
}
