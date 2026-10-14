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
)

func TestActivityCompositionPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser history customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	paths := []string{}
	for _, kind := range []string{"estimate", "invoice", "customer"} {
		fieldPost(t, router, cookie, "/settings/custom-fields", url.Values{"name": {"Browser observation"}, "object_type": {kind}, "field_type": {"text"}}, 303)
		id := c.ID
		if kind != "customer" {
			fieldPost(t, router, cookie, "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Browser history document"}, "invoice_date": {"2026-10-13"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
			if err := pool.QueryRow(ctx, `SELECT id FROM `+kind+`s`).Scan(&id); err != nil {
				t.Fatal(err)
			}
		}
		paths = append(paths, fmt.Sprintf("/%ss/%d", kind, id))
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	args := append([]string{"testdata/browser/activity-composition-repair.cjs", server.URL, cookie.Value}, paths...)
	out, err := exec.CommandContext(ctx, "node", args...).CombinedOutput()
	t.Logf("browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
