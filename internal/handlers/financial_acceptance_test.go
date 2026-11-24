package handlers

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/settlement"
)

// Use the installed browser boundary and a genuine rendered CSRF token. The
// strict decoder must run before authentication middleware's form reads.
func financialAcceptancePost(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values, want int) string {
	t.Helper()
	web, r := financialAcceptanceFormRequest(t, router, cookie, path, values)
	w := httptest.NewRecorder()
	web.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("POST %s: %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	return w.Header().Get("Location")
}

func financialAcceptanceFormRequest(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values) (http.Handler, *http.Request) {
	t.Helper()
	web := middleware.BrowserCSRF(middleware.CSRFToken(router))
	get := httptest.NewRecorder()
	web.ServeHTTP(get, httptest.NewRequest("GET", "/login", nil))
	match := regexp.MustCompile(`name="csrf_token" value="([^"]+)"`).FindStringSubmatch(get.Body.String())
	if get.Code != 200 || len(match) != 2 {
		t.Fatalf("CSRF login form: %d %s", get.Code, get.Body.String())
	}
	form := url.Values{}
	for k, v := range values {
		form[k] = append([]string(nil), v...)
	}
	form.Set("csrf_token", html.UnescapeString(match[1]))
	r := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "http://example.com")
	r.AddCookie(cookie)
	for _, c := range get.Result().Cookies() {
		r.AddCookie(c)
	}
	return web, r
}

// These are public route contracts, deliberately independent of handler dispatch.
var financialAcceptanceOutputs = []struct{ route, export, view, grant, facet string }{
	{"/reports/invoice-aging", "/reports/invoice-aging/export", "invoices", "customer.financial.export", "invoice.view"},
	{"/reports/invoice-aging", "/reports/customer-financials/export", "customers", "customer.financial.export", "invoice.view"},
	{"/reports/payments-credits", "/reports/settlements/export", "settlements", "settlement.export", "settlement.view"},
	{"/reports/payments-credits", "/reports/credits/export", "credits", "credit.export", "credit.view"},
	{"/reports/payments-credits", "/reports/payments-credits/export", "combined", "settlement.export", "settlement.view"},
}

func TestFinancialAcceptanceIndependentExportGrantsHTTP(t *testing.T) {
	for _, output := range financialAcceptanceOutputs {
		t.Run(output.view, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			u := client.User.Create().SetCompanyID(company).SetName("Independent finance reader").SetEmail("finance@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
			auth := authorization.New(pool)
			grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: output.facet, Scope: authorization.All}, {Permission: output.grant, Scope: authorization.All}}
			if output.view == "combined" {
				grants = append(grants, authorization.Grant{Permission: "credit.view", Scope: authorization.All}, authorization.Grant{Permission: "credit.export", Scope: authorization.All})
			}
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Independent finance", grants)
			historyCheck(t, err)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			values := url.Values{"operation_id": {newOperationKey()}}
			requestBody(t, web, cookie, "GET", output.route+"?view="+output.view, 200)
			requestBody(t, web, cookie, "GET", output.export, 200)
			path := financialAcceptancePost(t, web, cookie, output.export, values, 303)
			worked, err := exports.Product(pool).ProcessOne(ctx)
			historyCheck(t, err)
			if !worked {
				t.Fatal("accepted intent was not processed")
			}
			rows := timeExportCSV(t, requestBody(t, web, cookie, "GET", path+"/download", 200))
			if len(rows) == 0 {
				t.Fatal("authorized empty snapshot omitted typed zero totals")
			}
			// Direct current-grant DML, not a cached/mock principal mutation.
			_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, output.grant)
			historyCheck(t, err)
			financialAcceptancePost(t, web, cookie, output.export, url.Values{"operation_id": {newOperationKey()}}, 403)
			body := requestBody(t, web, cookie, "GET", path+"/download", 410)
			assertNotContains(t, body, "as_of,")
		})
	}
}

func TestFinancialAcceptanceOperationalAndFieldAPIDenial(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Operational only").SetEmail("operational-finance@test").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Operational only", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.export", Scope: authorization.All}})
	historyCheck(t, err)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	token, err := sessions.CreateMobile(ctx, admin, "Finance office exclusion")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	for _, output := range financialAcceptanceOutputs {
		path := output.route + "?view=" + output.view
		expectStatus(t, web, cookie, "GET", path, 403)
		expectStatus(t, web, cookie, "GET", output.export, 403)
		financialAcceptancePost(t, web, cookie, output.export, url.Values{"operation_id": {newOperationKey()}}, 403)
		for _, target := range []string{path, output.export} {
			for _, method := range []string{"GET", "POST"} {
				r := httptest.NewRequest(method, target, strings.NewReader("operation_id="+newOperationKey()))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				if w.Code != 404 && w.Code != 405 {
					t.Fatalf("field API finance response=%d %s", w.Code, w.Body.String())
				}
				w = httptest.NewRecorder()
				web.ServeHTTP(w, r)
				if w.Code < 300 || w.Code < 400 && !strings.Contains(w.Header().Get("Location"), "/login") {
					t.Fatalf("office accepted bearer: %d", w.Code)
				}
			}
		}
	}
	var jobs int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs`).Scan(&jobs))
	if jobs != 0 {
		t.Fatalf("denied requests accepted %d jobs", jobs)
	}
}

func TestFinancialAcceptanceRecordedCashCurrentCreditAndUnwindHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Cash conservation source").SaveX(ctx)
	svc := settlement.New(pool)
	a := authorization.Actor{UserID: admin, Channel: "web"}
	ctx = authorization.WithActor(ctx, authorization.New(pool), a)
	actor := settlement.Actor{ID: admin, CompanyID: company}
	payment, err := svc.RecordCustomerPayment(ctx, a, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 10000, Method: settlement.Cash, ReceivedDate: "2020-01-01"})
	historyCheck(t, err)
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT (now() AT TIME ZONE timezone)::date::text FROM company_settings WHERE company_id=$1`, company).Scan(&today))
	read := func(from string, cash, credit string) []map[string]string {
		t.Helper()
		values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "from": {from}, "to": {from}, "operation_id": {newOperationKey()}}
		path := financialAcceptancePost(t, web, cookie, "/reports/payments-credits/export", values, 303)
		_, err := exports.Product(pool).ProcessOne(ctx)
		historyCheck(t, err)
		rows := timeExportCSV(t, requestBody(t, web, cookie, "GET", path+"/download", 200))
		foundCash, foundCredit := false, false
		stamp := rows[0]["as_of"]
		for _, r := range rows {
			if r["as_of"] != stamp || stamp == "" {
				t.Fatal("mixed/missing snapshot stamps")
			}
			if r["row_type"] == "net_payments_total" && r["section"] == "period" {
				foundCash = true
				if r["cash_effect"] != cash {
					t.Fatalf("cash=%s want %s", r["cash_effect"], cash)
				}
			}
			if r["row_type"] == "current_credit_total" {
				foundCredit = true
				if r["current_available_amount"] != credit {
					t.Fatalf("credit=%s want %s", r["current_available_amount"], credit)
				}
			}
		}
		if !foundCash || !foundCredit {
			t.Fatal("missing independent cash/credit totals")
		}
		return rows
	}
	read(today, "100.00", "100.00")
	read("2020-01-01", "0.00", "100.00")
	refund, err := svc.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, CustomerID: c.ID, AmountCents: 2000, Method: settlement.Cash, EffectiveDate: settlement.Date(today), Reason: "Customer refund"})
	historyCheck(t, err)
	read(today, "80.00", "80.00")
	_, err = svc.ReverseRefund(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: refund.ID, CustomerID: c.ID, EffectiveDate: settlement.Date(today), Reason: "Refund correction"})
	historyCheck(t, err)
	read(today, "100.00", "100.00")
	_, err = svc.ReverseUnappliedPayment(ctx, a, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: payment.ID, CustomerID: c.ID, EffectiveDate: settlement.Date(today), Reason: "error"})
	historyCheck(t, err)
	rows := read(today, "0.00", "0.00")
	events := map[string]int{}
	for _, row := range rows {
		if row["row_type"] == "event" {
			events[row["event_type"]]++
		}
	}
	for _, kind := range []string{"payment", "credit_creation", "credit_refund", "refund_reversal", "payment_reversal"} {
		if events[kind] != 1 {
			t.Fatalf("immutable %s events=%d", kind, events[kind])
		}
	}
}
