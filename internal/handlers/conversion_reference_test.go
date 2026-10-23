package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestReviewedConversionReferencePersistedAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Conversion reference").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Conversion reference").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Conversion reference")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "../api/v1/testdata/conversion-reference-conformance.cjs", server.URL, token, fmt.Sprint(j.ID)).CombinedOutput()
	t.Logf("reference: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var cycles, active, invoices int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE reverted_at IS NULL),(SELECT count(*) FROM invoices) FROM estimate_invoice_conversion_cycles`).Scan(&cycles, &active, &invoices); err != nil {
		t.Fatal(err)
	}
	if cycles != 2 || active != 1 || invoices != 2 {
		t.Fatalf("persisted cycles=%d active=%d invoices=%d", cycles, active, invoices)
	}
}

func TestReviewedConversionPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser conversion").SaveX(ctx)
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Browser conversion source").SetTerms("Captured terms").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/conversion-reviewed.cjs", server.URL, cookie.Value, fmt.Sprint(e.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var cycles, active, invoices int
	if err := pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE reverted_at IS NULL),(SELECT count(*) FROM invoices WHERE estimate_id=$1) FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, e.ID).Scan(&cycles, &active, &invoices); err != nil {
		t.Fatal(err)
	}
	if cycles != 2 || active != 1 || invoices != 2 {
		t.Fatalf("persisted cycles=%d active=%d invoices=%d", cycles, active, invoices)
	}
}
