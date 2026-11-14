package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func paymentContractCall(t *testing.T, api http.Handler, token, method, path, body string, want int) string {
	t.Helper()
	w := boundedAPICall(api, token, method, path, body)
	if w.Code != want {
		t.Fatalf("%s %s=%d want%d: %s", method, path, w.Code, want, w.Body.String())
	}
	specResponse := httptest.NewRecorder()
	api.ServeHTTP(specResponse, httptest.NewRequest("GET", "/openapi.json", nil))
	var spec map[string]any
	if err := json.Unmarshal(specResponse.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://payment.test/openapi.json", spec); err != nil {
		t.Fatal(err)
	}
	name := "PaymentReceiptEnvelope"
	if want >= 400 {
		name = "ErrorEnvelope"
	}
	schema, err := compiler.Compile("https://payment.test/openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(w.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("%s contract: %v", name, err)
	}
	return w.Body.String()
}

func TestFieldPaymentServerContractAPI(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private payment customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Collection").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Field payment contract")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	cookie := sessionCookie(t, ctx, sessions, admin)
	newInvoice := func() int64 {
		t.Helper()
		raw := lifecycleCall(t, api, token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Private invoice","line_items":[{"title":"Work","quantity":"1","unit_price":"100.00"}]}`, newOperationKey(), newOperationKey(), j.ID, c.ID), 200)
		var response struct {
			Data struct{ ID int64 } `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &response); err != nil {
			t.Fatal(err)
		}
		if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, response.Data.ID); err != nil {
			t.Fatal(err)
		}
		return response.Data.ID
	}
	command := func(op, amount string) string {
		return fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"amount":%q,"method":"cash","date":"2026-01-02"}`, op, j.ID, amount)
	}
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("strict query contract", func(t *testing.T) {
		id, op := newInvoice(), newOperationKey()
		path := fmt.Sprintf("/invoices/%d/payments", id)
		paymentContractCall(t, api, token, "POST", path+"?amount=1.00", command(op, "25.00"), 400)
		paymentContractCall(t, api, token, "GET", "/payment-operations/"+op, "", 404)
		paymentContractCall(t, api, token, "GET", "/payment-operations/"+op+"?expand=invoice", "", 400)
		paymentContractCall(t, api, token, "GET", fmt.Sprintf("/invoices/%d/payment-methods?credit=true", id), "", 400)
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM operation_admissions WHERE operation_id=$1`, op).Scan(&count); err != nil || count != 0 {
			t.Fatalf("query rejection admitted operation: count=%d err=%v", count, err)
		}
	})
	t.Run("archived customer is terminal", func(t *testing.T) {
		id, op := newInvoice(), newOperationKey()
		path, body := fmt.Sprintf("/invoices/%d/payments", id), command(op, "25.00")
		// Model retained/inconsistent installed history, without pretending the
		// ordinary archive action permits archiving an outstanding Invoice.
		sql(`UPDATE customers SET deleted_at=now() WHERE id=$1`, c.ID)
		defer sql(`UPDATE customers SET deleted_at=NULL WHERE id=$1`, c.ID)
		response := paymentContractCall(t, api, token, "POST", path, body, 409)
		assertContains(t, response, `"code":"invoice_ineligible"`)
		assertContains(t, response, `"recovery_disposition":"review"`)
		receipt := paymentContractCall(t, api, token, "GET", "/payment-operations/"+op, "", 200)
		assertContains(t, receipt, `"outcome":"invoice_ineligible"`)
		sql(`UPDATE customers SET deleted_at=NULL WHERE id=$1`, c.ID)
		paymentContractCall(t, api, token, "POST", path, body, 409)
		if got := paymentContractCall(t, api, token, "GET", "/payment-operations/"+op, "", 200); got != receipt {
			t.Fatal("terminal receipt changed after lifecycle repair")
		}
		paymentContractCall(t, api, token, "POST", path, command(newOperationKey(), "100.00"), 200)
	})
	t.Run("receipt persistence failure rolls back every effect", func(t *testing.T) {
		id, op := newInvoice(), newOperationKey()
		path, body := fmt.Sprintf("/invoices/%d/payments", id), command(op, "100.00")
		sql(`CREATE FUNCTION field_receipt_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected receipt failure'; END $$; CREATE TRIGGER field_receipt_failure BEFORE INSERT ON bounded_payment_operations FOR EACH ROW EXECUTE FUNCTION field_receipt_failure()`)
		response := paymentContractCall(t, api, token, "POST", path, body, 503)
		assertContains(t, response, `"recovery_disposition":"retry"`)
		paymentContractCall(t, api, token, "GET", "/payment-operations/"+op, "", 404)
		var payments, allocations, admissions, audits, activities int
		var state string
		if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments WHERE invoice_id=$1),(SELECT count(*) FROM payment_invoice_allocations WHERE invoice_id=$1),(SELECT count(*) FROM operation_admissions WHERE operation_id=$2::uuid),(SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$2::text),(SELECT count(*) FROM activity_logs WHERE action='payment_recorded' AND object_type='invoice' AND object_id=$1),settlement_state FROM invoices WHERE id=$1`, id, op).Scan(&payments, &allocations, &admissions, &audits, &activities, &state); err != nil {
			t.Fatal(err)
		}
		if payments+allocations+admissions+audits+activities != 0 || state != "unpaid" {
			t.Fatalf("partial commit: %d/%d/%d/%d/%d state=%s", payments, allocations, admissions, audits, activities, state)
		}
		sql(`DROP TRIGGER field_receipt_failure ON bounded_payment_operations; DROP FUNCTION field_receipt_failure()`)
		accepted := paymentContractCall(t, api, token, "POST", path, body, 200)
		if replay := paymentContractCall(t, api, token, "POST", path, body, 200); replay != accepted {
			t.Fatal("same-ID retry duplicated or changed receipt")
		}
	})
	t.Run("office settlement wins contention without shrinking field intent", func(t *testing.T) {
		id, op := newInvoice(), newOperationKey()
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
		done := make(chan *httptest.ResponseRecorder, 1)
		office := make(chan *httptest.ResponseRecorder, 1)
		values := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"60.00"}, "method": {"check"}, "date": {"2026-01-02"}}
		go func() {
			r := httptest.NewRequest("POST", fmt.Sprintf("/invoices/%d/payments-with-credit", id), strings.NewReader(values.Encode())).WithContext(raceCtx)
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.AddCookie(cookie)
			w := httptest.NewRecorder()
			web.ServeHTTP(w, r)
			office <- w
		}()
		commercialWaiters(t, raceCtx, pool, blocker, 1)
		path := fmt.Sprintf("/invoices/%d/payments", id)
		body := command(op, "60.00")
		go func() { done <- boundedAPICall(api, token, "POST", path, body) }()
		commercialWaiters(t, raceCtx, pool, blocker, 2)
		// The authority fence deterministically orders the office writer first.
		// Its real HTTP request is queued before releasing the fence below.
		if err := tx.Commit(raceCtx); err != nil {
			t.Fatal(err)
		}
		if w := <-office; w.Code != 303 {
			t.Fatalf("office winner=%d: %s", w.Code, w.Body.String())
		}
		if w := <-done; w.Code != 422 {
			t.Fatalf("field loser=%d: %s", w.Code, w.Body.String())
		}
		paymentContractCall(t, api, token, "POST", path, body, 422)
		paymentContractCall(t, api, token, "POST", path, command(newOperationKey(), "40.00"), 200)
		var sum, credits int64
		if err := pool.QueryRow(ctx, `SELECT (SELECT sum(amount_cents) FROM payment_invoice_allocations WHERE invoice_id=$1),(SELECT count(*) FROM customer_credits cc JOIN invoice_payments p ON p.id=cc.source_payment_id WHERE p.invoice_id=$1)`, id).Scan(&sum, &credits); err != nil || sum != 10000 || credits != 0 {
			t.Fatalf("allocation=%d credits=%d err=%v", sum, credits, err)
		}
	})
	t.Run("independent authority method history and private recovery", func(t *testing.T) {
		id := newInvoice()
		path := fmt.Sprintf("/invoices/%d/payments", id)
		auth := authorization.New(pool)
		actor := authorization.Actor{UserID: admin}
		grants := []authorization.Grant{{Permission: "invoice.view", Scope: authorization.Assigned}}
		role, err := auth.SaveRole(ctx, actor, 0, "Field collector", grants)
		if err != nil {
			t.Fatal(err)
		}
		u := client.User.Create().SetCompanyID(company).SetEmail("field-collector@test").SetName("Collector").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
		if err := auth.AssignUser(ctx, actor, u.ID, role); err != nil {
			t.Fatal(err)
		}
		sql(`INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID)
		collector, err := sessions.CreateMobile(ctx, u.ID, "Independent collection")
		if err != nil {
			t.Fatal(err)
		}
		denied := command(newOperationKey(), "25.00")
		paymentContractCall(t, api, collector, "POST", path, denied, 403)
		grants = append(grants, authorization.Grant{Permission: "payment.record", Scope: authorization.Assigned})
		var revision int64
		if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.SaveRole(ctx, actor, role, "Field collector", grants, revision); err != nil {
			t.Fatal(err)
		}
		// Grant repair does not revive a terminal operation.
		paymentContractCall(t, api, collector, "POST", path, denied, 403)
		op := newOperationKey()
		date := time.Now().UTC().Format("2006-01-02")
		sql(`UPDATE company_settings SET timezone='UTC' WHERE company_id=$1`, company)
		body := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"amount":"25.00","method":"other","date":%q,"other_description":"Money order","reference":"private-reference","note":"private-note"}`, op, j.ID, date)
		documentTransactionRequest(t, web, cookie, "POST", "/settings/payment-methods/other", paymentMethodValues(t, web, cookie, "other", "Postal receipt"), 303)
		accepted := paymentContractCall(t, api, collector, "POST", path, body, 200)
		var before, after string
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM invoice_payments p WHERE invoice_id=$1`, id).Scan(&before); err != nil {
			t.Fatal(err)
		}
		for _, fact := range []string{`"amount_cents": 2500`, `"method": "other"`, `"method_label": "Postal receipt"`, `"other_description": "Money order"`, `"channel": "api"`, `"reference": "private-reference"`, `"notes": "private-note"`, fmt.Sprintf(`"actor_id": %d`, u.ID), fmt.Sprintf(`"customer_id": %d`, c.ID), `"received_date": "` + date + `"`} {
			assertContains(t, before, fact)
		}
		documentTransactionRequest(t, web, cookie, "POST", "/settings/payment-methods/other", paymentMethodValues(t, web, cookie, "other", "Renamed again"), 303)
		if replay := paymentContractCall(t, api, collector, "POST", path, body, 200); replay != accepted {
			t.Fatal("label rename changed staged identity")
		}
		if err := pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM invoice_payments p WHERE invoice_id=$1`, id).Scan(&after); err != nil || after != before {
			t.Fatalf("immutable history changed: %v", err)
		}
		for _, changed := range []string{strings.Replace(body, "25.00", "25.01", 1), strings.Replace(body, "private-note", "changed-note", 1)} {
			paymentContractCall(t, api, collector, "POST", path, changed, 409)
		}
		paymentContractCall(t, api, collector, "POST", fmt.Sprintf("/invoices/%d/payments", id+100000), body, 409)
		paymentContractCall(t, api, token, "GET", "/payment-operations/"+op, "", 404)
		// Queue admission behind an assignment removal and prove current authority
		// wins over the earlier effective date and the cached command.
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
		if _, err := tx.Exec(raceCtx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, u.ID); err != nil {
			t.Fatal(err)
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- boundedAPICall(api, collector, "POST", path, command(newOperationKey(), "25.00")) }()
		commercialWaiters(t, raceCtx, pool, blocker, 1)
		if err := tx.Commit(raceCtx); err != nil {
			t.Fatal(err)
		}
		if w := <-done; w.Code != 404 {
			t.Fatalf("lost assignment accepted: %d %s", w.Code, w.Body.String())
		}
		recovered := paymentContractCall(t, api, collector, "GET", "/payment-operations/"+op, "", 200)
		if recovered != accepted {
			t.Fatal("access loss changed original receipt")
		}
		for _, private := range []string{"Private", "private-reference", "private-note", "customer_id", "invoice_id", "25.00", "credit"} {
			assertNotContains(t, recovered, private)
		}
		fresh, err := sessions.CreateMobile(ctx, u.ID, "Fresh reconciliation")
		if err != nil {
			t.Fatal(err)
		}
		if paymentContractCall(t, api, fresh, "GET", "/payment-operations/"+op, "", 200) != accepted {
			t.Fatal("fresh authentication lost receipt")
		}
		paymentContractCall(t, api, fresh, "POST", path, body, 409)
	})
}
