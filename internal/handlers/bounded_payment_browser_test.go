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

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestBoundedPaymentLostResponsePersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser Payment Customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Browser fixed payment"}, "invoice_date": {time.Now().Format("2006-01-02")}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/bounded-payment.cjs", server.URL, cookie.Value, fmt.Sprint(id)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	var cents int64
	if err := pool.QueryRow(ctx, `SELECT count(*),sum(amount_cents) FROM invoice_payments`).Scan(&count, &cents); err != nil {
		t.Fatal(err)
	}
	if count != 1 || cents != 2500 {
		t.Fatalf("lost response repeated payment: count%d cents%d", count, cents)
	}
}
