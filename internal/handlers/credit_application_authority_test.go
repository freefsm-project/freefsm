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

func TestCreditApplicationIndependentAuthorityHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='Pacific/Kiritimati' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	base := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "credit.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Apply only", append(base, authorization.Grant{Permission: "credit.apply", Scope: authorization.All}))
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("apply-only@test").SetName("Credit clerk").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Credit authority").SaveX(ctx)
	documentTransactionRequest(t, web, adminCookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	documentTransactionRequest(t, web, adminCookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Credit authority"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice int64
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
	path := fmt.Sprintf("/invoices/%d/credit-applications", invoice)
	values := url.Values{"idempotency_key": {newOperationKey()}, "credit_id": {credit}, "amount": {"60.00"}, "effective_date": {"2026-01-02"}}
	page := documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d/credit", invoice), nil, 200)
	assertContains(t, page.Body.String(), "Apply Credit")
	if strings.Contains(page.Body.String(), "Application history") {
		t.Fatal("Apply grant disclosed history")
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION credit_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='credit.apply' THEN RAISE EXCEPTION 'injected Credit audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER credit_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION credit_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, web, cookie, "POST", path, values, 500)
	var effects int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM credit_applications)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)+(SELECT count(*) FROM settlement_idempotency WHERE operation='apply_credit')`, values.Get("idempotency_key")).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("audit rollback effects=%d", effects)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER credit_audit_failure ON authorization_audit; DROP FUNCTION credit_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Other source owner").SaveX(ctx)
	documentTransactionRequest(t, web, adminCookie, "POST", fmt.Sprintf("/customers/%d/payments", other.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	var foreignCredit string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE customer_id=$1`, other.ID).Scan(&foreignCredit))
	values.Set("credit_id", foreignCredit)
	foreign := documentTransactionRequest(t, web, cookie, "POST", path, values, 404)
	values.Set("credit_id", newOperationKey())
	missing := documentTransactionRequest(t, web, cookie, "POST", path, values, 404)
	if foreign.Body.String() != missing.Body.String() {
		t.Fatal("foreign source disclosed through validation")
	}
	values.Set("credit_id", credit)
	values.Set("effective_date", "2999-01-01")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 422)
	values.Set("effective_date", "2026-01-02")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
	applyKey := values.Get("idempotency_key")
	documentTransactionRequest(t, web, adminCookie, "GET", "/credit-application-operations/"+applyKey, nil, 404)
	var application string
	if err := pool.QueryRow(ctx, `SELECT id FROM credit_applications`).Scan(&application); err != nil {
		t.Fatal(err)
	}
	reverse := url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"Selected wrong Invoice"}, "reference": {"correction-52"}, "notes": {"No cash movement"}}
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+application+"/reverse", reverse, 403)
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Reverse only", append(base, authorization.Grant{Permission: "settlement.view", Scope: authorization.All}, authorization.Grant{Permission: "credit.application.reverse", Scope: authorization.All}), revision); err != nil {
		t.Fatal(err)
	}
	values.Set("idempotency_key", newOperationKey())
	values.Set("amount", "1.00")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 403)
	page = documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d/credit", invoice), nil, 200)
	assertContains(t, page.Body.String(), "Reverse Application")
	if strings.Contains(page.Body.String(), "<h2>Apply Credit") {
		t.Fatal("Reverse granted Apply action")
	}
	reverse.Set("effective_date", "2999-01-01")
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+application+"/reverse", reverse, 422)
	reverse.Set("effective_date", "2026-01-02")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION credit_reverse_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='credit.application.reverse' THEN RAISE EXCEPTION 'injected reversal audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER credit_reverse_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION credit_reverse_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+application+"/reverse", reverse, 500)
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM settlement_reversals)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)`, reverse.Get("idempotency_key")).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("reversal audit rollback effects=%d", effects)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER credit_reverse_audit_failure ON authorization_audit; DROP FUNCTION credit_reverse_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+application+"/reverse", reverse, 303)
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+application+"/reverse", reverse, 303)
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action IN ('credit.apply','credit.application.reverse') AND actor_id=$1`, u.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 2 {
		t.Fatalf("immutable application audits=%d want2", audits)
	}
	var reference, notes string
	if err := pool.QueryRow(ctx, `SELECT reference,notes FROM settlement_reversals WHERE operation_id=$1`, application).Scan(&reference, &notes); err != nil {
		t.Fatal(err)
	}
	if reference != "correction-52" || notes != "No cash movement" {
		t.Fatalf("reversal metadata=%q %q", reference, notes)
	}
	if _, err := pool.Exec(ctx, `UPDATE settlement_reversals SET notes='rewritten' WHERE operation_id=$1`, application); err == nil {
		t.Fatal("reversal metadata mutable")
	}
	reverse.Set("idempotency_key", newOperationKey())
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+application+"/reverse", reverse, 409)
	// Outcome ownership remains useful after action loss without revealing any
	// Invoice, selected source, amount, or reversal metadata.
	receipt := documentTransactionRequest(t, web, cookie, "GET", "/credit-application-operations/"+applyKey, nil, 200)
	assertContains(t, receipt.Body.String(), `"outcome":"accepted"`)
	if strings.Contains(receipt.Body.String(), credit) || strings.Contains(receipt.Body.String(), "amount") {
		t.Fatal("receipt leaked financial projection")
	}
	token, err := sessions.CreateMobile(ctx, admin, "Credit excluded")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	lifecycleCall(t, api, token, "POST", path, `{}`, 404)
	lifecycleCall(t, api, token, "POST", path+"/"+application+"/reverse", `{}`, 404)
}

func TestCreditApplicationCommitAuthorityHTTP(t *testing.T) {
	for _, change := range []struct{ action, revoke string }{{"credit.apply", "credit.apply"}, {"credit.application.reverse", "credit.application.reverse"}, {"credit.apply", "credit.view"}, {"credit.apply", "invoice.view"}} {
		t.Run(change.revoke, func(t *testing.T) {
			action := change.action
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Authority contender").SaveX(ctx)
			documentTransactionRequest(t, web, adminCookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
			documentTransactionRequest(t, web, adminCookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Authority contender"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
			var invoice int64
			var credit string
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice))
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits`).Scan(&credit))
			historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice))
			grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "credit.view", Scope: authorization.All}, {Permission: "settlement.view", Scope: authorization.All}, {Permission: action, Scope: authorization.All}}
			auth := authorization.New(pool)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Contending clerk", grants)
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetEmail("contending-credit@test").SetName("Contending clerk").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			path := fmt.Sprintf("/invoices/%d/credit-applications", invoice)
			values := url.Values{"idempotency_key": {newOperationKey()}, "credit_id": {credit}, "amount": {"60.00"}, "effective_date": {"2026-01-02"}, "reason": {"correction"}}
			wantApplications := 0
			if action == "credit.application.reverse" {
				documentTransactionRequest(t, web, adminCookie, "POST", path, values, 303)
				var id string
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM credit_applications`).Scan(&id))
				path += "/" + id + "/reverse"
				values.Set("idempotency_key", newOperationKey())
				wantApplications = 1
			}
			tx, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer tx.Rollback(context.Background())
			var blocker int
			historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker))
			_, err = tx.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, change.revoke)
			historyCheck(t, err)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode())).WithContext(ctx)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				r.AddCookie(cookie)
				w := httptest.NewRecorder()
				web.ServeHTTP(w, r)
				done <- w
			}()
			commercialWaiters(t, ctx, pool, blocker, 1)
			historyCheck(t, tx.Commit(ctx))
			w := <-done
			wantStatus := 403
			if change.revoke == "invoice.view" {
				wantStatus = 404
			}
			if w.Code != wantStatus {
				t.Fatalf("revoked contender=%d %s", w.Code, w.Body.String())
			}
			var applications, effects int
			historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM credit_applications),(SELECT count(*) FROM settlement_reversals)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)+(SELECT count(*) FROM authorization_audit WHERE actor_id=$2 AND action IN ('credit.apply','credit.application.reverse'))`, values.Get("idempotency_key"), u.ID).Scan(&applications, &effects))
			if applications != wantApplications || effects != 0 {
				t.Fatalf("revoked contender applications=%d effects=%d", applications, effects)
			}
		})
	}
}
