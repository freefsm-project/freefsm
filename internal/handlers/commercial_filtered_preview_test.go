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

	"github.com/freefsm-project/freefsm/internal/middleware"
)

func TestCommercialFilteredPreviewPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	_, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Filtered preview Customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/commercial-filtered-preview.cjs", server.URL, cookie.Value, fmt.Sprint(c.ID)).CombinedOutput()
	t.Logf("%s", output)
	if err != nil {
		t.Fatal(err)
	}
}
