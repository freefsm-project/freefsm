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

func TestBoundedPaymentRejectsOverpaymentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Bounded payment customer").SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Fixed payment"}, "invoice_date": {time.Now().Format("2006-01-02")}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", values, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	draftPage := documentTransactionRequest(t, router, cookie, "GET", fmt.Sprintf("/invoices/%d", id), nil, 200)
	assertContains(t, draftPage.Body.String(), `data-payment-eligible="false"`)
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d/payments", id)
	payment := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.01"}, "method": {"cash"}, "date": {time.Now().Format("2006-01-02")}}
	documentTransactionRequest(t, router, cookie, "POST", path, payment, 422)
	var receipts, allocations, credits int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments),(SELECT count(*) FROM payment_invoice_allocations),(SELECT count(*) FROM customer_credits)`).Scan(&receipts, &allocations, &credits); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || allocations != 0 || credits != 0 {
		t.Fatalf("rejected fixed intent created payment=%d allocation=%d credit=%d", receipts, allocations, credits)
	}
	payment.Set("idempotency_key", newOperationKey())
	payment.Set("amount", "100.00")
	documentTransactionRequest(t, router, cookie, "POST", path, payment, 303)
	var amount int64
	var manual, state string
	if err := pool.QueryRow(ctx, `SELECT a.amount_cents,s.category_key,i.settlement_state FROM invoices i JOIN statuses s ON s.id=i.status_id JOIN payment_invoice_allocations a ON a.invoice_id=i.id WHERE i.id=$1`, id).Scan(&amount, &manual, &state); err != nil {
		t.Fatal(err)
	}
	if amount != 10000 || manual != "invoice:invoiced" || state != "paid" {
		t.Fatalf("amount=%d manual=%s settlement=%s", amount, manual, state)
	}
}

func TestBoundedPaymentIndependentAssignedGrantHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Collect fixed payment", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "payment.record", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("bounded@test").SetName("Collector").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Assigned Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Assigned collection").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	documentTransactionRequest(t, web, adminCookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Assigned receipt"}, "invoice_date": {time.Now().Format("2006-01-02")}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	page := documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d", id), nil, 200)
	assertContains(t, page.Body.String(), "Record Payment")
	assertContains(t, page.Body.String(), "Bank Transfer")
	if strings.Contains(page.Body.String(), "Apply Customer Credit") {
		t.Fatal("bounded collector saw Credit controls")
	}
	op := newOperationKey()
	values := url.Values{"idempotency_key": {op}, "amount": {"25.00"}, "method": {"cash"}, "date": {"2026-01-02"}}
	path := fmt.Sprintf("/invoices/%d/payments", id)
	documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
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
	if _, err := tx.Exec(raceCtx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	racing := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"25.00"}, "method": {"cash"}, "date": {"2026-01-02"}}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", path, strings.NewReader(racing.Encode())).WithContext(raceCtx)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		done <- w
	}()
	commercialWaiters(t, raceCtx, pool, blocker, 1)
	if err := tx.Commit(raceCtx); err != nil {
		t.Fatal(err)
	}
	if w := <-done; w.Code != 404 {
		t.Fatalf("assignment-loss contender=%d: %s", w.Code, w.Body.String())
	}
	response := documentTransactionRequest(t, web, cookie, "GET", "/payment-operations/"+op, nil, 200)
	assertContains(t, response.Body.String(), `"outcome":"accepted"`)
	for _, text := range []string{"Assigned Customer", "Assigned receipt", "25.00", "customer_id", "credit"} {
		if strings.Contains(response.Body.String(), text) {
			t.Fatalf("receipt disclosure: %s", response.Body.String())
		}
	}
	values.Set("idempotency_key", newOperationKey())
	documentTransactionRequest(t, web, cookie, "POST", path, values, 404)
}

func TestBoundedPaymentReceiptAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Receipt Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Receipt Job").SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Bounded payment")
	if err != nil {
		t.Fatal(err)
	}
	created := lifecycleCall(t, api, token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Fixed receipt","line_items":[{"title":"Work","quantity":"1","unit_price":"100.00"}]}`, newOperationKey(), newOperationKey(), j.ID), 200)
	var document struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(created), &document); err != nil {
		t.Fatal(err)
	}
	id := document.Data.ID
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id); err != nil {
		t.Fatal(err)
	}
	op := newOperationKey()
	body := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"amount":"25.00","method":"check","date":"2026-01-02","reference":"check-17","note":"Fixed receipt"}`, op, j.ID)
	call := func(method, path, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s=%d want%d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	path := fmt.Sprintf("/invoices/%d/payments", id)
	accepted := call(http.MethodPost, path, body, 200)
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
	for name, raw := range map[string]string{"BoundedPaymentCommand": body, "PaymentReceiptEnvelope": accepted} {
		schema, err := compiler.Compile("https://payment.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	assertContains(t, accepted, `"outcome":"accepted"`)
	for _, forbidden := range []string{"check-17", "Fixed receipt", "customer_id", "credit", "25.00"} {
		if strings.Contains(accepted, forbidden) {
			t.Fatalf("sensitive receipt: %s", accepted)
		}
	}
	if replay := call("POST", path, body, 200); replay != accepted {
		t.Fatalf("receipt changed: %s / %s", accepted, replay)
	}
	call("POST", path, strings.Replace(body, "25.00", "25.01", 1), 409)
	if receipt := call("GET", "/payment-operations/"+op, "", 200); receipt != accepted {
		t.Fatal("lookup differs from accepted receipt")
	}
	var count, audits int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments),(SELECT count(*) FROM authorization_audit WHERE action='payment.record')`).Scan(&count, &audits); err != nil {
		t.Fatal(err)
	}
	if count != 1 || audits != 1 {
		t.Fatalf("payments=%d audits=%d", count, audits)
	}
}

func TestBoundedPaymentMethodLabelsPreserveHistoryHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Method Customer").SaveX(ctx)
	documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Method history"}, "invoice_date": {time.Now().Format("2006-01-02")}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, web, cookie, "POST", "/settings/payment-methods/other", url.Values{"label": {"Alternative receipt"}}, 303)
	page := documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d", id), nil, 200)
	assertContains(t, page.Body.String(), "Alternative receipt")
	values := url.Values{"idempotency_key": {newOperationKey()}, "amount": {"25.00"}, "method": {"other"}, "date": {"2026-01-02"}, "reference": {"external-17"}, "notes": {"Historical receipt"}}
	path := fmt.Sprintf("/invoices/%d/payments", id)
	documentTransactionRequest(t, web, cookie, "POST", path, values, 422)
	values.Set("idempotency_key", newOperationKey())
	values.Set("other_description", "Postal money order")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
	var before, after string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM invoice_payments p`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, fact := range []string{`"method": "other"`, `"method_label": "Alternative receipt"`, `"other_description": "Postal money order"`, `"channel": "web"`, `"received_date": "2026-01-02"`} {
		assertContains(t, before, fact)
	}
	documentTransactionRequest(t, web, cookie, "POST", "/settings/payment-methods/other", url.Values{"label": {"Miscellaneous"}}, 303)
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM invoice_payments p`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("renaming a method rewrote its receipt")
	}
	page = documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d", id), nil, 200)
	assertContains(t, page.Body.String(), "Alternative receipt")
	assertContains(t, page.Body.String(), "Miscellaneous")
	values.Set("idempotency_key", newOperationKey())
	values.Del("other_description")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 422)
}
