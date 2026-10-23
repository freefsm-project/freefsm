package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestOfficePaymentUnappliedReceiptHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	// Dates are company-local calendar dates, including reversal on the same
	// source date east of UTC; PostgreSQL DATE decoding is not an instant.
	_, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='Asia/Tokyo' WHERE company_id=$1`, company)
	historyCheck(t, err)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Unapplied receipt customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/customers/%d/payments", c.ID)
	v := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"123.45"}, "method": {"other"}, "other_description": {"Postal order"}, "date": {"2026-01-02"}, "reference": {"UNAPPLIED-1"}, "notes": {"Retainer"}}
	for _, invalid := range []struct {
		key, value string
		status     int
	}{{"amount", "0.00", 400}, {"amount", "-0.01", 400}, {"method", "credit", 422}, {"other_description", "", 422}, {"date", "2999-01-01", 422}} {
		old := v.Get(invalid.key)
		v.Set(invalid.key, invalid.value)
		documentTransactionRequest(t, web, cookie, "POST", path, v, invalid.status)
		v.Set(invalid.key, old)
	}
	documentTransactionRequest(t, web, cookie, "POST", path, v, 303)
	documentTransactionRequest(t, web, cookie, "POST", path, v, 303)
	var receipt, credit int64
	var count, allocations, invoices int
	var noInvoice bool
	if err := pool.QueryRow(ctx, `SELECT count(*),sum(p.amount_cents),sum(c.original_amount_cents),bool_and(p.invoice_id IS NULL),(SELECT count(*) FROM payment_invoice_allocations),(SELECT count(*) FROM invoices) FROM invoice_payments p JOIN customer_credits c ON c.source_payment_id=p.id`).Scan(&count, &receipt, &credit, &noInvoice, &allocations, &invoices); err != nil {
		t.Fatal(err)
	}
	if count != 1 || receipt != 12345 || credit != 12345 || !noInvoice || allocations != 0 || invoices != 0 {
		t.Fatalf("count%d receipt%d credit%d null%v allocations%d invoices%d", count, receipt, credit, noInvoice, allocations, invoices)
	}
	lookup := documentTransactionRequest(t, web, cookie, "GET", "/payment-operations/"+v.Get("idempotency_key"), nil, 200)
	assertContains(t, lookup.Body.String(), `"outcome":"accepted"`)
	page := documentTransactionRequest(t, web, cookie, "GET", path, nil, 200)
	for _, text := range []string{"Unapplied Payment", "123.45", "Postal order", "UNAPPLIED-1", "Retainer"} {
		assertContains(t, page.Body.String(), text)
	}
	dashboard := documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), nil, 200)
	assertContains(t, dashboard.Body.String(), "123.45")
	var payment string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM invoice_payments`).Scan(&payment); err != nil {
		t.Fatal(err)
	}
	reverse := url.Values{"idempotency_key": {newOperationKey()}, "date": {"2026-01-02"}, "reason": {"external_return"}, "notes": {"Returned retainer"}}
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", reverse, 303)
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", reverse, 303)
	page = documentTransactionRequest(t, web, cookie, "GET", path, nil, 200)
	assertContains(t, page.Body.String(), "Returned retainer")
	var remaining int64
	if err := pool.QueryRow(ctx, `SELECT coalesce(sum(c.original_amount_cents),0) FROM customer_credits c WHERE NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=c.source_payment_id)`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("reversed Unapplied source still available: %d", remaining)
	}
	if _, err := pool.Exec(ctx, `UPDATE invoice_payments SET amount_cents=1`); err == nil {
		t.Fatal("receipt amount was mutable")
	}
	if _, err := pool.Exec(ctx, `UPDATE invoice_payments SET customer_id=customer_id`); err == nil {
		t.Fatal("receipt customer was mutable")
	}
}

func TestOfficePaymentCustomerScopeAndReceivablesHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Receivable owner").SaveX(ctx)
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other owner").SaveX(ctx)
	ids := []int64{}
	for _, customer := range []int64{c.ID, other.ID} {
		documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(customer)}, "title": {"Keep outstanding"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
		var id int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE customer_id=$1`, customer).Scan(&id))
		ids = append(ids, id)
		historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id))
	}
	path := fmt.Sprintf("/customers/%d/payments", c.ID)
	page := documentTransactionRequest(t, web, cookie, "GET", path, nil, 200)
	assertContains(t, page.Body.String(), fmt.Sprintf(`value="%d"`, ids[0]))
	assertNotContains(t, page.Body.String(), fmt.Sprintf(`value="%d"`, ids[1]))
	financial := regexp.MustCompile(`(?s)<article class="dashboard-panel customer-dashboard-financial">.*?</article>`)
	before := financial.FindString(documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), nil, 200).Body.String())
	if before == "" {
		t.Fatal("missing financial projection")
	}
	v := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"125.00"}, "method": {"cash"}, "date": {"2026-01-02"}, "invoice_id": {fmt.Sprint(ids[1])}}
	documentTransactionRequest(t, web, cookie, "POST", path, v, 404)
	v.Del("invoice_id")
	documentTransactionRequest(t, web, cookie, "POST", path, v, 303)
	after := financial.FindString(documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/customers/%d", c.ID), nil, 200).Body.String())
	if before != after {
		t.Fatal("Unapplied receipt changed receivables or aging")
	}
	var state string
	historyCheck(t, pool.QueryRow(ctx, `SELECT settlement_state FROM invoices WHERE id=$1`, ids[0]).Scan(&state))
	if state != "unpaid" {
		t.Fatalf("Unapplied marked Invoice %s", state)
	}
	// Existing explicit application consumes the selected source; reversing the
	// originating receipt must remain blocked until that dependency is unwound.
	var payment, credit string
	historyCheck(t, pool.QueryRow(ctx, `SELECT source_payment_id::text,id::text FROM customer_credits`).Scan(&payment, &credit))
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/invoices/%d/credit-applications", ids[0]), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"20.00"}, "credit_id": {credit}, "effective_date": {"2026-01-02"}}, 303)
	reverse := url.Values{"idempotency_key": {newOperationKey()}, "reason": {"external_return"}, "date": {"2026-01-02"}}
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", reverse, 409)
	var application string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id::text FROM credit_applications`).Scan(&application))
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/invoices/%d/credit-applications/%s/reverse", ids[0], application), url.Values{"idempotency_key": {newOperationKey()}, "reason": {"Undo application"}, "effective_date": {"2026-01-02"}}, 303)
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", reverse, 303)
	// Customer-scoped reversal cannot use an Invoice route or another Customer.
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/customers/%d/payments/%s/reverse", other.ID, payment), url.Values{"idempotency_key": {newOperationKey()}, "reason": {"error"}, "date": {"2026-01-02"}}, 404)
}

func TestOfficePaymentUnappliedCurrentAuthorityHTTP(t *testing.T) {
	for _, change := range []string{"grant_loss", "customer_archive"} {
		t.Run(change, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Unapplied authority").SaveX(ctx)
			auth := authorization.New(pool)
			grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "payment.credit", Scope: authorization.All}}
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Receipt only", grants)
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetEmail("unapplied@test").SetName("Collector").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			path := fmt.Sprintf("/customers/%d/payments", c.ID)
			page := documentTransactionRequest(t, web, cookie, "GET", path, nil, 200)
			assertNotContains(t, page.Body.String(), "Receipt history")
			// Neither settlement history nor Credit View nor bounded collection is an
			// implicit grant from the office receipt action.
			v := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"12.34"}, "method": {"cash"}, "date": {"2026-01-02"}}
			tx, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer tx.Rollback(context.Background())
			var blocker int
			historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker))
			if change == "grant_loss" {
				_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='payment.credit'`, role)
			} else {
				_, err = tx.Exec(ctx, `UPDATE customers SET deleted_at=now() WHERE id=$1`, c.ID)
			}
			historyCheck(t, err)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode())).WithContext(ctx)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				web.ServeHTTP(w, r)
				done <- w
			}()
			commercialWaiters(t, ctx, pool, blocker, 1)
			historyCheck(t, tx.Commit(ctx))
			w := <-done
			if w.Code != 403 && w.Code != 404 {
				t.Fatalf("revoked contender %d: %s", w.Code, w.Body.String())
			}
			var effects int
			historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments)+(SELECT count(*) FROM customer_credits)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)`, v.Get("idempotency_key")).Scan(&effects))
			if effects != 0 {
				t.Fatalf("ineligible receipt left %d effects", effects)
			}
		})
	}
}

func TestOfficePaymentIndependentViewsAndUnappliedAtomicityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent receipt facets").SaveX(ctx)
	path := fmt.Sprintf("/customers/%d/payments", c.ID)
	auth := authorization.New(pool)
	base := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Only receive money", append(base, authorization.Grant{Permission: "payment.credit", Scope: authorization.All}))
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("receive-only@test").SetName("Independent collector").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	op := newOperationKey()
	v := url.Values{"idempotency_key": {op}, "amount": {"56.78"}, "method": {"check"}, "date": {"2026-01-02"}, "reference": {"PRIVATE RETAINER"}}
	_, err = pool.Exec(ctx, `CREATE FUNCTION unapplied_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.credit' THEN RAISE EXCEPTION 'injected Unapplied audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER unapplied_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION unapplied_audit_failure()`)
	historyCheck(t, err)
	documentTransactionRequest(t, web, cookie, "POST", path, v, 500)
	var effects int
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments)+(SELECT count(*) FROM customer_credits)+(SELECT count(*) FROM settlement_idempotency)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)`, op).Scan(&effects))
	if effects != 0 {
		t.Fatalf("failed Unapplied leaked %d effects", effects)
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER unapplied_audit_failure ON authorization_audit; DROP FUNCTION unapplied_audit_failure()`)
	historyCheck(t, err)
	// Lost-response concurrent retries of one receipt, not two receipts of money.
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			web.ServeHTTP(w, r)
			done <- w
		}()
	}
	for range 2 {
		w := <-done
		if w.Code != 303 {
			t.Fatalf("duplicate contender%d: %s", w.Code, w.Body.String())
		}
	}
	var payments, credits, audits int
	var label, channel string
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments),(SELECT count(*) FROM customer_credits),(SELECT count(*) FROM authorization_audit WHERE action='payment.credit'),method_label,channel FROM invoice_payments`).Scan(&payments, &credits, &audits, &label, &channel))
	if payments != 1 || credits != 1 || audits != 1 || label != "Check" || channel != "web" {
		t.Fatalf("payments%d credits%d audits%d label%s channel%s", payments, credits, audits, label, channel)
	}
	page := documentTransactionRequest(t, web, cookie, "GET", path, nil, 200)
	assertNotContains(t, page.Body.String(), "PRIVATE RETAINER")
	assertNotContains(t, page.Body.String(), "Receipt history")
	var payment string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id::text FROM invoice_payments`).Scan(&payment))
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", url.Values{"idempotency_key": {newOperationKey()}, "date": {"2026-01-02"}, "reason": {"No grant"}}, 403)
	for _, permission := range []string{"settlement.view", "credit.view"} {
		readRole, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, permission, append(base, authorization.Grant{Permission: permission, Scope: authorization.All}))
		historyCheck(t, err)
		reader := client.User.Create().SetCompanyID(company).SetEmail(permission + "@test").SetName("Independent reader").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
		historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, reader.ID, readRole))
		readCookie := sessionCookie(t, ctx, sessions, reader.ID)
		v.Set("idempotency_key", newOperationKey())
		documentTransactionRequest(t, web, readCookie, "POST", path, v, 403)
		if permission == "settlement.view" {
			p := documentTransactionRequest(t, web, readCookie, "GET", path, nil, 200)
			assertContains(t, p.Body.String(), "PRIVATE RETAINER")
			assertNotContains(t, p.Body.String(), "data-office-payment>")
		} else {
			documentTransactionRequest(t, web, readCookie, "GET", path, nil, 403)
			p := documentTransactionRequest(t, web, readCookie, "GET", fmt.Sprintf("/customers/%d", c.ID), nil, 200)
			assertContains(t, p.Body.String(), "56.78")
			assertNotContains(t, p.Body.String(), "PRIVATE RETAINER")
		}
		documentTransactionRequest(t, web, readCookie, "GET", "/payment-operations/"+op, nil, 404)
	}
}
