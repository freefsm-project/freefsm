package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func boundedAPICall(api http.Handler, token, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestBoundedPaymentFaultRollbackValidationAndConcurrencyAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Fault Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Fault Job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Fault payment")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	created := lifecycleCall(t, api, token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Fault payment","line_items":[{"title":"Work","quantity":"1","unit_price":"100.00"}]}`, newOperationKey(), newOperationKey(), j.ID, c.ID), 200)
	var document struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(created), &document); err != nil {
		t.Fatal(err)
	}
	id := document.Data.ID
	path := fmt.Sprintf("/invoices/%d/payments", id)
	command := func(op, amount, method, date string) string {
		return fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"amount":%q,"method":%q,"date":%q}`, op, j.ID, amount, method, date)
	}
	call := func(method, path, body string, want int) string {
		t.Helper()
		w := boundedAPICall(api, token, method, path, body)
		if w.Code != want {
			t.Fatalf("%s %s=%d want%d %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	call("POST", path, command(newOperationKey(), "1.00", "cash", "2026-01-02"), 409) // Draft cannot receive money.
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct{ amount, method, date string }{{"0.00", "cash", "2026-01-02"}, {"-1.00", "cash", "2026-01-02"}, {"1.001", "cash", "2026-01-02"}, {"NaN", "cash", "2026-01-02"}, {"1.00", "wire", "2026-01-02"}, {"1.00", "other", "2026-01-02"}, {"1.00", "cash", "2999-01-01"}, {"100.01", "cash", "2026-01-02"}} {
		op := newOperationKey()
		body := command(op, invalid.amount, invalid.method, invalid.date)
		call("POST", path, body, 422)
		assertContains(t, call("GET", "/payment-operations/"+op, "", 200), `"outcome":"invalid_payment"`)
		call("POST", path, body, 422)
	}
	op := newOperationKey()
	body := command(op, "25.00", "cash", "2026-01-02")
	if _, err := pool.Exec(ctx, `CREATE FUNCTION bounded_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.record' THEN RAISE EXCEPTION 'injected bounded audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER bounded_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION bounded_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	call("POST", path, body, 503)
	call("GET", "/payment-operations/"+op, "", 404)
	var effects, allocations, admissions int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments),(SELECT count(*) FROM payment_invoice_allocations),(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)`, op).Scan(&effects, &allocations, &admissions); err != nil {
		t.Fatal(err)
	}
	if effects != 0 || allocations != 0 || admissions != 0 {
		t.Fatalf("audit failure leaked payment%d allocation%d admission%d", effects, allocations, admissions)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER bounded_audit_failure ON authorization_audit; DROP FUNCTION bounded_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	call("POST", path, body, 200)
	call("POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Reused financial intent","line_items":[]}`, op, newOperationKey(), j.ID), 409)
	// Two new fixed Payments exceed the remaining75 in combination. Exactly one
	// whole60 receipt commits; the loser cannot shrink to15 or create Credit.
	done := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() {
			done <- boundedAPICall(api, token, "POST", path, command(newOperationKey(), "60.00", "transfer", "2026-01-02"))
		}()
	}
	counts := map[int]int{}
	for range 2 {
		w := <-done
		counts[w.Code]++
	}
	if counts[200] != 1 || counts[422] != 1 {
		t.Fatalf("concurrent dispositions=%v", counts)
	}
	var sum, credits int64
	var state, manual string
	if err := pool.QueryRow(ctx, `SELECT (SELECT sum(amount_cents) FROM payment_invoice_allocations),(SELECT count(*) FROM customer_credits),i.settlement_state,s.category_key FROM invoices i JOIN statuses s ON s.id=i.status_id WHERE i.id=$1`, id).Scan(&sum, &credits, &state, &manual); err != nil {
		t.Fatal(err)
	}
	if sum != 8500 || credits != 0 || state != "partially_paid" || manual != "invoice:invoiced" {
		t.Fatalf("sum%d credits%d state%s manual%s", sum, credits, state, manual)
	}
}
