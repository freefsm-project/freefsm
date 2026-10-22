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

func TestOfficePaymentSelectedAndUnappliedPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser office Customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Browser office invoice"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/fixture/revoke-office-session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "method", 405)
			return
		}
		if err := authorization.New(pool).RevokeSessions(r.Context(), authorization.Actor{UserID: admin}, admin, 0); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(200)
	})
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/office-payment.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID), fmt.Sprint(invoice)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var received, allocated, credit int64
	var count int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments),(SELECT sum(amount_cents) FROM invoice_payments),(SELECT sum(amount_cents) FROM payment_invoice_allocations),(SELECT sum(original_amount_cents) FROM customer_credits)`).Scan(&count, &received, &allocated, &credit); err != nil {
		t.Fatal(err)
	}
	if count != 2 || received != 16513 || allocated != 10000 || credit != 6513 {
		t.Fatalf("count%d receipts%d allocated%d credit%d", count, received, allocated, credit)
	}
}
