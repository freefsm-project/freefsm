package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestCreditApplicationExactAmountHTTP(t *testing.T) {
	for _, tc := range []struct {
		name, source, amount string
		finalized            bool
		status               int
	}{
		{"source_exceeded", "40.00", "40.01", true, 422},
		{"due_exceeded", "125.00", "100.01", true, 422},
		{"draft", "125.00", "10.00", false, 409},
		{"exact_source", "40.00", "40.00", true, 303},
		{"exact_due", "125.00", "100.00", true, 303},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Selected source").SaveX(ctx)
			documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {tc.source}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
			documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Credit destination"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
			var invoice int64
			var credit string
			if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT id FROM customer_credits`).Scan(&credit); err != nil {
				t.Fatal(err)
			}
			if tc.finalized {
				if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice); err != nil {
					t.Fatal(err)
				}
			}
			key := newOperationKey()
			values := url.Values{"idempotency_key": {key}, "credit_id": {credit}, "amount": {tc.amount}, "effective_date": {"2026-01-02"}}
			path := fmt.Sprintf("/invoices/%d/credit-applications", invoice)
			documentTransactionRequest(t, web, cookie, "POST", path, values, tc.status)
			if tc.status == 303 {
				documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
			}
			var applications, receipts, admission int
			var applied, received int64
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM credit_applications),(SELECT coalesce(sum(amount_cents),0) FROM credit_applications),(SELECT count(*) FROM invoice_payments),(SELECT sum(amount_cents) FROM invoice_payments),(SELECT count(*) FROM settlement_idempotency WHERE operation='apply_credit' AND idempotency_key=$1)`, key).Scan(&applications, &applied, &receipts, &received, &admission); err != nil {
				t.Fatal(err)
			}
			wantSource, _ := parseCents(tc.source)
			if receipts != 1 || received != wantSource {
				t.Fatalf("Application changed cash receipts: count=%d cash=%d", receipts, received)
			}
			if tc.status == 303 {
				want, _ := parseCents(tc.amount)
				if applications != 1 || applied != want || admission != 1 {
					t.Fatalf("exact Application/replay: count=%d amount=%d receipts=%d", applications, applied, admission)
				}
			} else if applications != 0 || applied != 0 || admission != 0 {
				t.Fatalf("rejected Application leaked effects: count=%d amount=%d receipts=%d", applications, applied, admission)
			}
		})
	}
}

func TestCreditApplicationContentionAndRestorationHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Contended Credit").SaveX(ctx)
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Credit destination"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice, manualStatus int64
	var originalFinancial string
	var credit string
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM customer_credits`).Scan(&credit); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status_id,financial FROM invoices WHERE id=$1`, invoice).Scan(&manualStatus, &originalFinancial); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d/credit-applications", invoice)
	values := url.Values{"idempotency_key": {newOperationKey()}, "credit_id": {credit}, "amount": {"60.00"}, "effective_date": {"2026-01-02"}}
	// A persistence failure after inserting the Application must roll back the
	// record, projection, source consumption and replay receipt together.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION application_activity_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='credit_applied' THEN RAISE EXCEPTION 'injected application activity failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER application_activity_failure BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION application_activity_failure()`); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, web, cookie, "POST", path, values, 500)
	var effects int
	var state string
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM credit_applications)+(SELECT count(*) FROM settlement_idempotency WHERE operation='apply_credit'),settlement_state FROM invoices WHERE id=$1`, invoice).Scan(&effects, &state); err != nil {
		t.Fatal(err)
	}
	if effects != 0 || state != "unpaid" {
		t.Fatalf("rollback effects=%d state=%s", effects, state)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER application_activity_failure ON activity_logs; DROP FUNCTION application_activity_failure()`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blocker int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			v := url.Values{"idempotency_key": {newOperationKey()}, "credit_id": {credit}, "amount": {"60.00"}, "effective_date": {"2026-01-02"}}
			r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode())).WithContext(ctx)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			web.ServeHTTP(w, r)
			done <- w
		}()
	}
	commercialWaiters(t, ctx, pool, blocker, 2)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	statuses := map[int]int{}
	for range 2 {
		w := <-done
		statuses[w.Code]++
	}
	if statuses[303] != 1 || statuses[422] != 1 {
		t.Fatalf("contenders=%v; want one exact acceptance, one rejection", statuses)
	}
	var application string
	var amount int64
	if err := pool.QueryRow(ctx, `SELECT id,amount_cents FROM credit_applications`).Scan(&application, &amount); err != nil {
		t.Fatal(err)
	}
	if amount != 6000 {
		t.Fatalf("accepted amount=%d", amount)
	}
	if _, err := pool.Exec(ctx, `UPDATE invoices SET notes='rewrite during settlement' WHERE id=$1`, invoice); err == nil {
		t.Fatal("active Application did not lock commercial content")
	}
	reversal := url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"Wrong allocation"}}
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+application+"/reverse", reversal, 303)
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+application+"/reverse", reversal, 303)
	var restoredStatus int64
	if err := pool.QueryRow(ctx, `SELECT status_id,settlement_state FROM invoices WHERE id=$1`, invoice).Scan(&restoredStatus, &state); err != nil {
		t.Fatal(err)
	}
	if restoredStatus != manualStatus || state != "unpaid" {
		t.Fatalf("restoration status=%d want=%d state=%s", restoredStatus, manualStatus, state)
	}
	var intact, locked bool
	if err := pool.QueryRow(ctx, `SELECT financial=$2,invoice_has_active_settlement(id) FROM invoices WHERE id=$1`, invoice, originalFinancial).Scan(&intact, &locked); err != nil {
		t.Fatal(err)
	}
	if !intact || locked {
		t.Fatalf("reversal changed commercial past or retained settlement lock: intact=%v locked=%v", intact, locked)
	}
	if _, err := pool.Exec(ctx, `UPDATE invoices SET notes='Authorized correction after reversal' WHERE id=$1`, invoice); err != nil {
		t.Fatalf("last reversal did not unlock commercial content: %v", err)
	}
	// The same selected source can now cover the full original due. Neither
	// compensation nor reapplication may create another cash receipt.
	values.Set("amount", "100.00")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
	var cash, original, reversed int64
	if err := pool.QueryRow(ctx, `SELECT (SELECT sum(amount_cents) FROM invoice_payments),(SELECT amount_cents FROM credit_applications WHERE id=$1),(SELECT count(*) FROM settlement_reversals WHERE operation_id=$1)`, application).Scan(&cash, &original, &reversed); err != nil {
		t.Fatal(err)
	}
	if cash != 10000 || original != 6000 || reversed != 1 {
		t.Fatalf("cash=%d original=%d reversals=%d", cash, original, reversed)
	}
}
