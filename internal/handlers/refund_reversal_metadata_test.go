package handlers

import (
	"context"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
	"net/url"
	"testing"
)

func TestRefundPaymentReversalMetadataHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("External return").SaveX(ctx)
	path := fmt.Sprintf("/customers/%d/payments", c.ID)
	documentTransactionRequest(t, web, cookie, "POST", path, url.Values{"idempotency_key": {newOperationKey()}, "amount": {"10.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	var payment string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoice_payments`).Scan(&payment))
	v := url.Values{"idempotency_key": {newOperationKey()}, "date": {"2026-01-03"}, "reason": {"arbitrary"}, "reference": {"return-53"}, "notes": {"Returned outside FreeFSM"}}
	invalid := documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", v, 422)
	assertContains(t, invalid.Body.String(), "Payment reversal reason")
	v.Set("reason", "external_return")
	for _, date := range []string{"", "2026-01-01", "2999-01-01"} {
		v.Set("date", date)
		documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", v, 422)
	}
	v.Set("date", "2026-01-03")
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", v, 303)
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", v, 303)
	var reference, notes string
	historyCheck(t, pool.QueryRow(ctx, `SELECT reference,notes FROM settlement_reversals`).Scan(&reference, &notes))
	if reference != "return-53" || notes != "Returned outside FreeFSM" {
		t.Fatalf("metadata lost: %q %q", reference, notes)
	}
	v.Set("notes", "different")
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", v, 409)
	for _, reason := range []string{"error", "failure", "chargeback"} {
		documentTransactionRequest(t, web, cookie, "POST", path, url.Values{"idempotency_key": {newOperationKey()}, "amount": {"10.00"}, "method": {"cash"}, "date": {"2026-01-02"}, "reference": {reason}}, 303)
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoice_payments WHERE reference=$1`, reason).Scan(&payment))
		v.Set("idempotency_key", newOperationKey())
		v.Set("reason", reason)
		documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment+"/reverse", v, 303)
	}
}

func TestRefundInstalledReversalAdmissionHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed reversal").SaveX(ctx)
	path := fmt.Sprintf("/customers/%d/payments", c.ID)
	documentTransactionRequest(t, web, cookie, "POST", path, url.Values{"idempotency_key": {newOperationKey()}, "amount": {"10.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	var payment uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoice_payments`).Scan(&payment))
	key := newOperationKey()
	result, err := settlement.New(pool).ReversePayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.ReverseRequest{Operation: settlement.Operation{Key: key}, ID: payment, CustomerID: c.ID, EffectiveDate: "2026-01-03", Reason: "Historical prose retained"})
	historyCheck(t, err)
	v := url.Values{"idempotency_key": {key}, "date": {"2026-01-03"}, "reason": {"Historical prose retained"}}
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment.String()+"/reverse", v, 303)
	receipt := documentTransactionRequest(t, web, cookie, "GET", "/refund-operations/"+key, nil, 200)
	assertCreditApplicationReceipt(t, receipt, key, result.ID.String(), "payment_reversal")
	v.Set("reason", "error")
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+payment.String()+"/reverse", v, 409)
	v.Set("reason", "Historical prose retained")
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Other corrections", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "settlement.view", Scope: authorization.All}, {Permission: "payment.reverse", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetName("Other actor").SetEmail("other-reversal@test").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	other := sessionCookie(t, ctx, sessions, u.ID)
	documentTransactionRequest(t, web, other, "POST", path+"/"+payment.String()+"/reverse", v, 409)
	documentTransactionRequest(t, web, other, "GET", "/refund-operations/"+key, nil, 404)
}

func TestRefundFieldAPIExcludesOfficeWriters(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	token, err := sessions.CreateMobile(context.Background(), admin, "Office exclusions")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	for _, path := range []string{"/customers/1/credit/refunds", "/customers/1/credit/refunds/" + newOperationKey() + "/reverse", "/customers/1/payments/" + newOperationKey() + "/reverse", "/invoices/1/payments/" + newOperationKey() + "/reverse", "/invoices/1/credit-applications", "/invoices/1/credit-applications/" + newOperationKey() + "/reverse", "/invoices/1/payments-with-credit", "/customers/1/payments"} {
		lifecycleCall(t, api, token, "POST", path, `{}`, 404)
	}
	for _, path := range []string{"/refund-operations/" + newOperationKey(), "/customers/1/refunds"} {
		lifecycleCall(t, api, token, "GET", path, ``, 404)
	}
}
