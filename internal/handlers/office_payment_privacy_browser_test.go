package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestOfficePaymentCurrentProjectionPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	for _, scenario := range []string{"history", "invoice", "customer", "entry", "refresh-failure", "submit-history", "submit-customer"} {
		t.Run(scenario, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE CUSTOMER IDENTITY").SaveX(ctx)
			adminCookie := sessionCookie(t, ctx, sessions, admin)
			documentTransactionRequest(t, web, adminCookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Private Invoice"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":173.29}]`}, "tax_rate": {"0"}}, 303)
			var invoice int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice))
			historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice))
			auth := authorization.New(pool)
			grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "payment.credit", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "settlement.view", Scope: authorization.All}}
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Office privacy review", grants)
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetEmail("privacy@test").SetName("Office collector").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			path := fmt.Sprintf("/customers/%d/payments", c.ID)
			documentTransactionRequest(t, web, cookie, "POST", path, url.Values{"idempotency_key": {newOperationKey()}, "amount": {"12.34"}, "method": {"cash"}, "date": {"2026-01-02"}, "reference": {"PRIVATE HISTORICAL REFERENCE"}}, 303)
			mux := http.NewServeMux()
			mux.HandleFunc("/fixture/change-office-authority", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "method", 405)
					return
				}
				var revision int64
				if err := pool.QueryRow(r.Context(), `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				filtered := []authorization.Grant{}
				for _, g := range grants {
					if (scenario == "history" || scenario == "submit-history") && g.Permission == "settlement.view" {
						continue
					}
					if scenario == "invoice" && g.Permission == "invoice.view" {
						continue
					}
					if scenario == "entry" && g.Permission == "payment.credit" {
						continue
					}
					filtered = append(filtered, g)
				}
				if scenario == "customer" || scenario == "submit-customer" {
					filtered = nil
				}
				if _, err := auth.SaveRole(r.Context(), authorization.Actor{UserID: admin}, role, "Office privacy review", filtered, revision); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				w.WriteHeader(200)
			})
			mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
			mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
			server := httptest.NewServer(mux)
			defer server.Close()
			out, err := exec.CommandContext(ctx, "node", "testdata/browser/office-payment-privacy.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID), fmt.Sprint(invoice), scenario).CombinedOutput()
			t.Logf("browser: %s", out)
			if err != nil {
				t.Fatal(err)
			}
			var count int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoice_payments`).Scan(&count))
			want := 2
			if scenario == "submit-customer" {
				want = 1
			}
			if count != want {
				t.Fatalf("receipts=%d want%d", count, want)
			}
		})
	}
}
