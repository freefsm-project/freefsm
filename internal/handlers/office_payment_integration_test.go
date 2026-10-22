package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestOfficePaymentInvoiceSplitHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Office receipt").SaveX(ctx)
	documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Overpayment"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d/payments-with-credit", invoice)
	values := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"125.01"}, "method": {"cash"}, "date": {"2026-01-02"}}
	documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
	documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
	lookup := documentTransactionRequest(t, web, cookie, "GET", "/payment-operations/"+values.Get("idempotency_key"), nil, 200)
	assertContains(t, lookup.Body.String(), `"outcome":"accepted"`)
	values.Set("amount", "125.02")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 409)
	values.Set("amount", "125.01")
	var receipt, allocated, credit int64
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*),sum(p.amount_cents),sum(a.amount_cents),sum(c.original_amount_cents) FROM invoice_payments p JOIN payment_invoice_allocations a ON a.payment_id=p.id JOIN customer_credits c ON c.source_payment_id=p.id`).Scan(&count, &receipt, &allocated, &credit); err != nil {
		t.Fatal(err)
	}
	if count != 1 || receipt != 12501 || allocated != 10000 || credit != 2501 {
		t.Fatalf("count=%d receipt=%d allocated=%d credit=%d", count, receipt, allocated, credit)
	}
	for _, statement := range []string{`UPDATE payment_invoice_allocations SET amount_cents=1`, `UPDATE payment_invoice_allocations SET invoice_id=invoice_id`, `UPDATE customer_credits SET original_amount_cents=1`} {
		if _, err := pool.Exec(ctx, statement); err == nil {
			t.Fatalf("financial original mutable: %s", statement)
		}
	}
	page := documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d", invoice), nil, 200)
	assertContains(t, page.Body.String(), "125.01")
	values.Set("idempotency_key", newOperationKey())
	documentTransactionRequest(t, web, cookie, "POST", path, values, 422)
}

func TestOfficePaymentEligibilityRecheckedAfterWaitHTTP(t *testing.T) {
	for _, change := range []string{"customer_archive", "invoice_archive", "invoice_void"} {
		t.Run(change, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Race customer").SaveX(ctx)
			documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Race invoice"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
			var invoice int64
			if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice); err != nil {
				t.Fatal(err)
			}
			if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice); err != nil {
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
			switch change {
			case "customer_archive":
				_, err = tx.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, c.ID)
			case "invoice_archive":
				_, err = tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`)
				if err == nil {
					_, err = tx.Exec(ctx, `UPDATE invoices SET status_id=(SELECT id FROM statuses WHERE category_key='invoice:void' LIMIT 1),deleted_at=now() WHERE id=$1`, invoice)
				}
			case "invoice_void":
				_, err = tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`)
				if err == nil {
					_, err = tx.Exec(ctx, `UPDATE invoices SET status_id=(SELECT id FROM statuses WHERE category_key='invoice:void' LIMIT 1) WHERE id=$1`, invoice)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				v := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"125.00"}, "method": {"cash"}, "date": {"2026-01-02"}}
				r := httptest.NewRequest("POST", fmt.Sprintf("/invoices/%d/payments-with-credit", invoice), strings.NewReader(v.Encode())).WithContext(ctx)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				web.ServeHTTP(w, r)
				done <- w
			}()
			commercialWaiters(t, ctx, pool, blocker, 1)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			w := <-done
			if w.Code != 403 && w.Code != 409 {
				t.Fatalf("ineligible contender=%d: %s", w.Code, w.Body.String())
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments)+(SELECT count(*) FROM customer_credits)`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("ineligible contender left %d effects", count)
			}
		})
	}
}

func TestOfficePaymentAuthorityFailureAndContentionHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "payment.credit", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Office collection", grants)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("office-credit@test").SetName("Collector").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Office authority").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Office job").SaveX(ctx)
	documentTransactionRequest(t, web, adminCookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Office contention"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d/payments-with-credit", invoice)
	values := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"60.00"}, "method": {"transfer"}, "date": {"2026-01-02"}}
	documentTransactionRequest(t, web, cookie, "POST", path, values, 409) // Draft.
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice); err != nil {
		t.Fatal(err)
	}
	values.Set("allocation", "1.00")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 400) // No discretionary allocation input.
	values.Del("allocation")
	for _, invalid := range []struct {
		key, value string
		status     int
	}{{"amount", "0.00", 400}, {"amount", "-1.00", 400}, {"method", "credit", 422}, {"method", "other", 422}, {"date", "2999-01-01", 422}} {
		old := values.Get(invalid.key)
		values.Set(invalid.key, invalid.value)
		documentTransactionRequest(t, web, cookie, "POST", path, values, invalid.status)
		values.Set(invalid.key, old)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION office_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.credit' THEN RAISE EXCEPTION 'injected office audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER office_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION office_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, web, cookie, "POST", path, values, 500)
	var effects int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments)+(SELECT count(*) FROM customer_credits)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)`, values.Get("idempotency_key")).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("rollback leaked %d effects", effects)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER office_audit_failure ON authorization_audit; DROP FUNCTION office_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	// Broad office grants do not enable bounded collection or field overpayment.
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/invoices/%d/payments", invoice), values, 403)
	token, err := sessions.CreateMobile(ctx, admin, "Office boundary")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	lifecycleCall(t, api, token, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), `{}`, 404)
	lifecycleCall(t, api, token, "POST", fmt.Sprintf("/invoices/%d/payments-with-credit", invoice), `{}`, 404)
	lifecycleCall(t, api, token, "POST", fmt.Sprintf("/invoices/%d/payments", invoice), fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"amount":"125.00","method":"cash","date":"2026-01-02"}`, newOperationKey(), j.ID), 422)
	// Two actual HTTP commands contend behind the shared financial fence. The
	// second receipt keeps its full amount but allocates only the remaining40.
	raceCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tx, err := pool.Begin(raceCtx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var blocker int
	if err := tx.QueryRow(raceCtx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			v := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"60.00"}, "method": {"cash"}, "date": {"2026-01-02"}}
			r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode())).WithContext(raceCtx)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			web.ServeHTTP(w, r)
			done <- w
		}()
	}
	commercialWaiters(t, raceCtx, pool, blocker, 2)
	if err := tx.Commit(raceCtx); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if w := <-done; w.Code != 303 {
			t.Fatalf("contender %d: %s", w.Code, w.Body.String())
		}
	}
	var received, allocated, credit int64
	if err := pool.QueryRow(ctx, `SELECT (SELECT sum(amount_cents) FROM invoice_payments),(SELECT sum(amount_cents) FROM payment_invoice_allocations),(SELECT sum(original_amount_cents) FROM customer_credits)`).Scan(&received, &allocated, &credit); err != nil {
		t.Fatal(err)
	}
	if received != 12000 || allocated != 10000 || credit != 2000 {
		t.Fatalf("received=%d allocated=%d credit=%d", received, allocated, credit)
	}
	// Current action revocation denies another receipt.
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Office collection", grants[:3], revision); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, web, cookie, "POST", path, values, 403)
}
