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

	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestCommercialPositiveQuantityPrecisionBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Tiny quantity Customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/commercial-quantity.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("%s", output)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"estimate", "invoice"} {
		var count int
		var raw string
		if err := pool.QueryRow(ctx, `SELECT count(*),min(financial) FROM `+kind+`s`).Scan(&count, &raw); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("%s invalid quantity writes persisted: count=%d", kind, count)
		}
		s, err := commercial.Read(raw)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Input.Lines) != 1 || s.Input.Lines[0].Quantity != "0.0000001" || s.Result.TotalCents != 1 {
			t.Fatalf("%s exact small quantity changed: %+v", kind, s)
		}
	}
}
