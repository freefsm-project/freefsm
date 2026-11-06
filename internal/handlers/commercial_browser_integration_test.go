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

func TestCommercialReferencePersistedAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reference customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reference Job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Commercial reference acceptance")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "../api/v1/testdata/commercial-reference-conformance.cjs", server.URL, token, fmt.Sprint(j.ID), fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("reference: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM estimates)+(SELECT count(*) FROM invoices)`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("lost responses duplicated documents: %d", count)
	}
}

func TestCommercialPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser commercial customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	out, err := exec.CommandContext(ctx, "node", "testdata/browser/commercial-documents.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var correct bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoices WHERE title='Browser invoice' AND notes='' AND due_date='0001-01-01'::date AND custom_fields->0->>'value'='Revised browser field' AND line_items->0->>'title'='Browser captured work' AND (line_items->0->>'unit_price')::numeric=12.50 AND (line_items->0->>'quantity')::numeric=1.5) AND EXISTS(SELECT 1 FROM estimates WHERE title='Browser estimate' AND notes='First browser edit' AND custom_fields->0->>'value'='Revised browser field' AND line_items->0->>'title'='Browser captured work')`).Scan(&correct); err != nil {
		t.Fatal(err)
	}
	if !correct {
		t.Fatal("browser effects were not persisted")
	}
}
