package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestCreditApplicationPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	for _, scenario := range []string{"reverse-only", "revoke-view"} {
		t.Run(scenario, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Credit browser").SaveX(ctx)
			documentTransactionRequest(t, web, adminCookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
			documentTransactionRequest(t, web, adminCookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Credit browser"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
			var invoice int64
			var credit string
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice))
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits`).Scan(&credit))
			historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice))
			auth := authorization.New(pool)
			base := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "credit.view", Scope: authorization.All}}
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Apply browser", append(base, authorization.Grant{Permission: "credit.apply", Scope: authorization.All}))
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetEmail("credit-browser@test").SetName("Credit browser clerk").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			mux := http.NewServeMux()
			mux.HandleFunc("/fixture/change-credit-authority", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "method", 405)
					return
				}
				var revision int64
				if err := pool.QueryRow(r.Context(), `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				var grants []authorization.Grant
				if scenario == "reverse-only" {
					grants = append(base, authorization.Grant{Permission: "settlement.view", Scope: authorization.All}, authorization.Grant{Permission: "credit.application.reverse", Scope: authorization.All})
				}
				if _, err := auth.SaveRole(r.Context(), authorization.Actor{UserID: admin}, role, "Changed Credit authority", grants, revision); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				w.WriteHeader(200)
			})
			mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
			mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
			server := httptest.NewServer(mux)
			defer server.Close()
			out, err := exec.CommandContext(ctx, "node", "testdata/browser/credit-applications.cjs", server.URL, cookie.Value, fmt.Sprint(invoice), credit, scenario).CombinedOutput()
			t.Logf("browser: %s", out)
			historyCheck(t, err)
			var count, reversals int
			var cash int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM credit_applications),(SELECT count(*) FROM settlement_reversals),(SELECT sum(amount_cents) FROM invoice_payments)`).Scan(&count, &reversals, &cash))
			want := 0
			if scenario == "reverse-only" {
				want = 1
			}
			if count != 1 || reversals != want || cash != 10000 {
				t.Fatalf("applications=%d reversals=%d cash=%d", count, reversals, cash)
			}
		})
	}
}
