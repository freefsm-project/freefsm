package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type creditCommand struct {
	path   string
	values url.Values
}
type creditMatrix struct {
	pool              *pgxpool.Pool
	web               http.Handler
	cookie            *http.Cookie
	ctx               context.Context
	customer, invoice int64
	actor             settlement.Actor
	source, payment   uuid.UUID
}

func newCreditMatrix(t *testing.T) *creditMatrix {
	t.Helper()
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := &creditMatrix{pool: pool, web: web, ctx: ctx, cookie: sessionCookie(t, ctx, sessions, admin), actor: settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Credit matrix").SaveX(ctx)
	f.customer = c.ID
	f.post(t, creditCommand{fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}}, 303)
	historyCheck(t, pool.QueryRow(ctx, `SELECT id,source_payment_id FROM customer_credits`).Scan(&f.source, &f.payment))
	f.invoice = f.newInvoice(t, "100")
	return f
}
func (f *creditMatrix) newInvoice(t *testing.T, amount string) int64 {
	t.Helper()
	f.post(t, creditCommand{"/invoices", url.Values{"customer_id": {fmt.Sprint(f.customer)}, "title": {"Matrix destination"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":` + amount + `}]`}, "tax_rate": {"0"}}}, 303)
	var id int64
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT max(id) FROM invoices`).Scan(&id))
	historyCheck(t, statusflow.New(f.pool).FinalizeInvoice(f.ctx, statusflow.Actor{ID: f.actor.ID, CompanyID: f.actor.CompanyID}, id))
	return id
}
func (f *creditMatrix) apply(invoice int64, amount string) creditCommand {
	return creditCommand{fmt.Sprintf("/invoices/%d/credit-applications", invoice), url.Values{"idempotency_key": {newOperationKey()}, "credit_id": {f.source.String()}, "amount": {amount}, "effective_date": {"2026-01-02"}}}
}
func (f *creditMatrix) reverse(t *testing.T, amount string) creditCommand {
	t.Helper()
	f.post(t, f.apply(f.invoice, amount), 303)
	var id string
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_applications WHERE invoice_id=$1`, f.invoice).Scan(&id))
	return creditCommand{fmt.Sprintf("/invoices/%d/credit-applications/%s/reverse", f.invoice, id), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"matrix compensation"}}}
}
func (f *creditMatrix) call(c creditCommand) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", c.path, strings.NewReader(c.values.Encode())).WithContext(f.ctx)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(f.cookie)
	w := httptest.NewRecorder()
	f.web.ServeHTTP(w, r)
	return w
}
func (f *creditMatrix) post(t *testing.T, c creditCommand, want int) {
	t.Helper()
	if strings.HasSuffix(c.path, "/convert") || strings.HasSuffix(c.path, "/revert-to-estimate") {
		fieldPost(t, f.web, f.cookie, c.path, c.values, want)
		return
	}
	w := f.call(c)
	if w.Code != want {
		t.Fatalf("POST %s = %d want%d: %s", c.path, w.Code, want, w.Body.String())
	}
}

// Queue two real requests on the production authority fence. Observe the first
// backend before starting the second, then prove PostgreSQL reports the first as
// a soft blocker of the second. This pins both serialization orders deliberately;
// no elapsed-time assumption establishes the race.
func (f *creditMatrix) ordered(t *testing.T, first, second creditCommand) (*httptest.ResponseRecorder, *httptest.ResponseRecorder) {
	t.Helper()
	tx, err := f.pool.Begin(f.ctx)
	historyCheck(t, err)
	defer tx.Rollback(context.Background())
	var blocker, firstPID int
	historyCheck(t, tx.QueryRow(f.ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker))
	a, b := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() { a <- f.call(first) }()
	commercialWaiters(t, f.ctx, f.pool, blocker, 1)
	app := f.pool.Config().ConnConfig.RuntimeParams["application_name"]
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, blocker, app).Scan(&firstPID))
	go func() { b <- f.call(second) }()
	commercialWaiters(t, f.ctx, f.pool, blocker, 2)
	var secondPID int
	var blockers []int32
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT pid,pg_blocking_pids(pid) FROM pg_stat_activity WHERE application_name=$2 AND pid<>$3 AND $1=ANY(pg_blocking_pids(pid)) AND $3=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, blocker, app, firstPID).Scan(&secondPID, &blockers))
	t.Logf("observed fence190019 holder=%d first=%d second=%d second_blockers=%v first_path=%s second_path=%s", blocker, firstPID, secondPID, blockers, first.path, second.path)
	historyCheck(t, tx.Commit(f.ctx))
	return <-a, <-b
}

func (f *creditMatrix) balances(t *testing.T, due, available, cash int64) {
	t.Helper()
	svc := settlement.New(f.pool)
	i, err := svc.InvoiceSettlement(f.ctx, f.actor, f.invoice)
	historyCheck(t, err)
	c, err := svc.CustomerSettlement(f.ctx, f.actor, f.customer)
	historyCheck(t, err)
	var received int64
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT coalesce(sum(p.amount_cents),0) FROM invoice_payments p WHERE NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=p.id)`).Scan(&received))
	if i.AmountDueCents != due || c.AvailableCreditCents != available || received != cash {
		t.Fatalf("due=%d Credit=%d cash=%d want %d/%d/%d", i.AmountDueCents, c.AvailableCreditCents, received, due, available, cash)
	}
	for _, s := range c.Sources {
		if s.AvailableCents < 0 {
			t.Fatalf("overspent source %+v", s)
		}
	}
}

func TestCreditApplicationMixedFinancialContentionHTTP(t *testing.T) {
	for _, operation := range []string{"apply", "reverse"} {
		for _, other := range []string{"payment", "refund", "application", "source-reversal"} {
			for _, primaryFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s_vs_%s/primary_first_%v", operation, other, primaryFirst), func(t *testing.T) {
					f := newCreditMatrix(t)
					primary := f.apply(f.invoice, "60.00")
					if operation == "reverse" {
						primary = f.reverse(t, "60.00")
					}
					var contender creditCommand
					switch other {
					case "payment":
						contender = creditCommand{fmt.Sprintf("/invoices/%d/payments-with-credit", f.invoice), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"60.00"}, "method": {"cash"}, "date": {"2026-01-02"}}}
					case "refund":
						contender = creditCommand{fmt.Sprintf("/customers/%d/credit/refunds", f.customer), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"60.00"}, "method": {"cash"}, "effective_date": {"2026-01-02"}, "reason": {"external refund"}}}
					case "application":
						contender = f.apply(f.newInvoice(t, "100"), "60.00")
					case "source-reversal":
						contender = creditCommand{fmt.Sprintf("/customers/%d/payments/%s/reverse", f.customer, f.payment), url.Values{"idempotency_key": {newOperationKey()}, "date": {"2026-01-02"}, "reason": {"external_return"}}}
					}
					var p, c *httptest.ResponseRecorder
					if primaryFirst {
						p, c = f.ordered(t, primary, contender)
					} else {
						c, p = f.ordered(t, contender, primary)
					}
					wantP, wantC := 303, 303
					var due, available, cash int64 = 10000, 10000, 10000
					if operation == "apply" {
						if primaryFirst {
							due, available = 4000, 4000
							switch other {
							case "payment":
								due, available, cash = 0, 6000, 16000
							case "refund", "application":
								wantC = 422
							case "source-reversal":
								wantC = 409
							}
						}
						if !primaryFirst {
							switch other {
							case "payment":
								wantP = 422
								due, cash = 4000, 16000
							case "refund", "application":
								wantP = 422
								available = 4000
							case "source-reversal":
								wantP = 404
								available, cash = 0, 0
							}
						}
					} else {
						if primaryFirst {
							switch other {
							case "payment":
								due, cash = 4000, 16000
							case "refund", "application":
								available = 4000
							case "source-reversal":
								available, cash = 0, 0
							}
						}
						if !primaryFirst {
							switch other {
							case "payment":
								due, available, cash = 6000, 12000, 16000
							case "refund", "application":
								wantC = 422
							case "source-reversal":
								wantC = 409
							}
						}
					}
					if p.Code != wantP || c.Code != wantC {
						t.Fatalf("primary=%d want%d %s; contender=%d want%d %s", p.Code, wantP, p.Body.String(), c.Code, wantC, c.Body.String())
					}
					f.balances(t, due, available, cash)
				})
			}
		}
	}
}

func (f *creditMatrix) edit(t *testing.T, amount string) creditCommand {
	t.Helper()
	var number int64
	historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT invoice_number FROM invoices WHERE id=$1`, f.invoice).Scan(&number))
	path := fmt.Sprintf("/invoices/%d", f.invoice)
	values := url.Values{"customer_id": {fmt.Sprint(f.customer)}, "invoice_number": {fmt.Sprint(number)}, "title": {"Reviewed edit"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":` + amount + `}]`}, "tax_rate": {"0"}}
	return creditCommand{path, commercialReviewedValues(t, f.web, f.cookie, path, values)}
}

func TestCreditApplicationCommercialEditContentionHTTP(t *testing.T) {
	for _, operation := range []string{"apply", "reverse"} {
		for _, primaryFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/primary_first_%v", operation, primaryFirst), func(t *testing.T) {
				f := newCreditMatrix(t)
				primary := f.apply(f.invoice, "60.00")
				if operation == "reverse" {
					primary = f.reverse(t, "60.00")
				}
				edit := f.edit(t, "50")
				var p, e *httptest.ResponseRecorder
				if primaryFirst {
					p, e = f.ordered(t, primary, edit)
				} else {
					e, p = f.ordered(t, edit, primary)
				}
				if operation == "apply" {
					if primaryFirst {
						if p.Code != 303 || (e.Code != 422 && e.Code != 409) {
							t.Fatalf("apply=%d edit=%d %s", p.Code, e.Code, e.Body.String())
						}
						f.balances(t, 4000, 4000, 10000)
					} else {
						if e.Code != 303 || p.Code != 422 {
							t.Fatalf("edit=%d %s apply=%d %s", e.Code, e.Body.String(), p.Code, p.Body.String())
						}
						f.balances(t, 5000, 10000, 10000)
					}
				} else {
					// Reversal advances the commercial revision; even an unlocked edit
					// must not silently accept an earlier reviewed revision.
					if p.Code != 303 || (e.Code != 409 && e.Code != 422) {
						t.Fatalf("reverse=%d %s edit=%d %s", p.Code, p.Body.String(), e.Code, e.Body.String())
					}
					f.balances(t, 10000, 10000, 10000)
					f.post(t, f.edit(t, "50"), 303)
					f.balances(t, 5000, 10000, 10000)
				}
			})
		}
	}
}

func TestCreditApplicationArchiveContentionHTTP(t *testing.T) {
	for _, kind := range []string{"customer", "invoice"} {
		for _, primaryFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("reverse_vs_%s/primary_first_%v", kind, primaryFirst), func(t *testing.T) {
				f := newCreditMatrix(t)
				reverse := f.reverse(t, "100.00")
				archive := creditCommand{fmt.Sprintf("/invoices/%d/delete", f.invoice), nil}
				if kind == "customer" {
					var preview struct{ Snapshot string }
					base := fmt.Sprintf("/customers/%d", f.customer)
					masterJSON(t, f.web, f.cookie, base+"/archive", &preview)
					archive = creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}
				}
				var r, a *httptest.ResponseRecorder
				if primaryFirst {
					r, a = f.ordered(t, reverse, archive)
				} else {
					a, r = f.ordered(t, archive, reverse)
				}
				if primaryFirst {
					if r.Code != 303 || a.Code != 409 {
						t.Fatalf("reverse=%d %s archive=%d %s", r.Code, r.Body.String(), a.Code, a.Body.String())
					}
					f.balances(t, 10000, 10000, 10000)
				} else {
					if a.Code != 303 || r.Code != 409 {
						t.Fatalf("archive=%d %s reverse=%d %s", a.Code, a.Body.String(), r.Code, r.Body.String())
					}
					f.balances(t, 0, 0, 10000)
					// History is readable, but a fresh reversal must follow an explicit
					// authorized restore. Restore does not itself create Credit or due.
					documentTransactionRequest(t, f.web, f.cookie, "GET", fmt.Sprintf("/invoices/%d/credit", f.invoice), nil, 200)
					id := f.invoice
					if kind == "customer" {
						id = f.customer
					}
					f.post(t, creditCommand{fmt.Sprintf("/%ss/%d/restore", kind, id), nil}, 303)
					f.balances(t, 0, 0, 10000)
					reverse.values.Set("idempotency_key", newOperationKey())
					f.post(t, reverse, 303)
					f.balances(t, 10000, 10000, 10000)
				}
			})
		}
	}
	// Apply and Customer archive can meet only with a previously reviewed
	// archive snapshot: a positive-due Invoice or available source blocks fresh
	// archive review. Revalidate that snapshot after each financial command.
	for _, primaryFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("apply_vs_customer/primary_first_%v", primaryFirst), func(t *testing.T) {
			f := newCreditMatrix(t)
			reverse := f.reverse(t, "100.00")
			var preview struct{ Snapshot string }
			base := fmt.Sprintf("/customers/%d", f.customer)
			masterJSON(t, f.web, f.cookie, base+"/archive", &preview)
			f.post(t, reverse, 303)
			apply := f.apply(f.invoice, "100.00")
			archive := creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}
			var p, a *httptest.ResponseRecorder
			if primaryFirst {
				p, a = f.ordered(t, apply, archive)
			} else {
				a, p = f.ordered(t, archive, apply)
			}
			wantArchive := 409
			if primaryFirst {
				wantArchive = 303
			}
			if p.Code != 303 || a.Code != wantArchive {
				t.Fatalf("apply=%d %s archive=%d want%d %s", p.Code, p.Body.String(), a.Code, wantArchive, a.Body.String())
			}
			f.balances(t, 0, 0, 10000)
		})
	}
}

func TestCreditApplicationPaidInvoiceArchiveContentionHTTP(t *testing.T) {
	for _, applyFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(applyFirst), func(t *testing.T) {
			f := newCreditMatrix(t)
			f.post(t, f.apply(f.invoice, "100.00"), 303)
			apply := f.apply(f.invoice, "1.00")
			archive := creditCommand{fmt.Sprintf("/invoices/%d/delete", f.invoice), nil}
			var p, a *httptest.ResponseRecorder
			if applyFirst {
				p, a = f.ordered(t, apply, archive)
			} else {
				a, p = f.ordered(t, archive, apply)
			}
			want := 409
			if applyFirst {
				want = 422
			}
			if p.Code != want || a.Code != 303 {
				t.Fatalf("apply=%d want%d %s archive=%d %s", p.Code, want, p.Body.String(), a.Code, a.Body.String())
			}
			f.balances(t, 0, 0, 10000)
		})
	}
}

func TestCreditApplicationConcurrentReversalIdentityHTTP(t *testing.T) {
	for _, sameKey := range []bool{true, false} {
		t.Run(fmt.Sprint(sameKey), func(t *testing.T) {
			f := newCreditMatrix(t)
			first := f.reverse(t, "60.00")
			second := creditCommand{first.path, url.Values{}}
			for k, v := range first.values {
				second.values[k] = append([]string(nil), v...)
			}
			if !sameKey {
				second.values.Set("idempotency_key", newOperationKey())
			}
			a, b := f.ordered(t, first, second)
			want := 409
			if sameKey {
				want = 303
			}
			if a.Code != 303 || b.Code != want {
				t.Fatalf("first=%d second=%d want%d", a.Code, b.Code, want)
			}
			f.balances(t, 10000, 10000, 10000)
			var originals, reversals int
			historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM credit_applications),(SELECT count(*) FROM settlement_reversals WHERE operation_type='credit_application')`).Scan(&originals, &reversals))
			if originals != 1 || reversals != 1 {
				t.Fatalf("originals=%d reversals=%d", originals, reversals)
			}
		})
	}
}

func TestCreditApplicationCompensatingRestorationContentionHTTP(t *testing.T) {
	for _, kind := range []string{"refund", "payment"} {
		for _, operation := range []string{"apply", "reverse"} {
			for _, primaryFirst := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s_vs_reverse_%s/primary_first_%v", operation, kind, primaryFirst), func(t *testing.T) {
					f := newCreditMatrix(t)
					var other creditCommand
					if kind == "refund" {
						path := fmt.Sprintf("/customers/%d/credit/refunds", f.customer)
						f.post(t, creditCommand{path, url.Values{"idempotency_key": {newOperationKey()}, "amount": {"60.00"}, "method": {"cash"}, "effective_date": {"2026-01-02"}, "reason": {"external refund"}}}, 303)
						var id string
						historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_refunds`).Scan(&id))
						other = creditCommand{path + "/" + id + "/reverse", url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"refund correction"}}}
					} else {
						path := fmt.Sprintf("/invoices/%d/payments-with-credit", f.invoice)
						f.post(t, creditCommand{path, url.Values{"idempotency_key": {newOperationKey()}, "amount": {"60.00"}, "method": {"cash"}, "date": {"2026-01-02"}}}, 303)
						var id string
						historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM invoice_payments WHERE invoice_id=$1`, f.invoice).Scan(&id))
						other = creditCommand{fmt.Sprintf("/invoices/%d/payments/%s/reverse", f.invoice, id), url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"error"}, "notes": {"payment correction"}}}
					}
					primary := f.apply(f.invoice, "60.00")
					if operation == "reverse" {
						primary = f.reverse(t, "40.00")
					}
					var p, c *httptest.ResponseRecorder
					if primaryFirst {
						p, c = f.ordered(t, primary, other)
					} else {
						c, p = f.ordered(t, other, primary)
					}
					want := 303
					if operation == "apply" && primaryFirst {
						want = 422
					}
					if p.Code != want || c.Code != 303 {
						t.Fatalf("primary=%d want%d %s compensation=%d %s", p.Code, want, p.Body.String(), c.Code, c.Body.String())
					}
					if operation == "apply" && !primaryFirst {
						f.balances(t, 4000, 4000, 10000)
					} else {
						f.balances(t, 10000, 10000, 10000)
					}
				})
			}
		}
	}
}
