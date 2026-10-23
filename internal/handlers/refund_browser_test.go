package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestRefundPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	for _, scenario := range []string{"reverse-only", "revoke-view"} {
		t.Run(scenario, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			ac := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Refund browser").SaveX(ctx)
			for _, s := range []struct{ amount, date string }{{"40.00", "2026-01-02"}, {"80.00", "2026-01-03"}} {
				documentTransactionRequest(t, web, ac, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {s.amount}, "method": {"cash"}, "date": {s.date}}, 303)
			}
			auth := authorization.New(pool)
			base := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "credit.view", Scope: authorization.All}}
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Refund clerk", append(base, authorization.Grant{Permission: "credit.refund", Scope: authorization.All}))
			historyCheck(t, err)
			u := client.User.Create().SetCompanyID(company).SetName("Refund clerk").SetEmail("refund-browser@test").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
			cookie := sessionCookie(t, ctx, sessions, u.ID)
			mux := http.NewServeMux()
			mux.HandleFunc("/fixture/refund-authority", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" {
					http.Error(w, "method", 405)
					return
				}
				var rev int64
				historyCheck(t, pool.QueryRow(r.Context(), `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&rev))
				var grants []authorization.Grant
				if scenario == "reverse-only" && r.URL.Query().Get("revoke") != "1" {
					grants = append(base, authorization.Grant{Permission: "settlement.view", Scope: authorization.All}, authorization.Grant{Permission: "credit.refund.reverse", Scope: authorization.All}, authorization.Grant{Permission: "payment.reverse", Scope: authorization.All})
				}
				_, err := auth.SaveRole(r.Context(), authorization.Actor{UserID: admin}, role, "Changed refund authority", grants, rev)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				w.WriteHeader(200)
			})
			mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
			mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
			server := httptest.NewServer(mux)
			defer server.Close()
			out, err := exec.CommandContext(ctx, "node", "testdata/browser/refunds.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID), scenario).CombinedOutput()
			t.Logf("browser: %s", out)
			historyCheck(t, err)
			var refunds, reversals int
			historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM credit_refunds),(SELECT count(*) FROM settlement_reversals)`).Scan(&refunds, &reversals))
			want := 0
			if scenario == "reverse-only" {
				want = 2
			}
			if refunds != 1 || reversals != want {
				t.Fatalf("effects=%d/%d", refunds, reversals)
			}
		})
	}
}
