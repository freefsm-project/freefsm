package templates

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/a-h/templ"
)

// Opt-in so the ordinary Go suite does not require Node or a browser.
func TestDocumentLineEnterBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("see testdata/browser/README.md to run the real-browser regression")
	}
	const existing = `[{"item_id":0,"title":"First distinct line","description":"First description","quantity":2,"unit_price":11,"discount":1,"surcharge":2,"taxable":false,"tax_rate":""},{"item_id":0,"title":"Second distinct line","description":"Second description","quantity":3,"unit_price":23,"discount":2,"surcharge":1,"taxable":false,"tax_rate":""}]`
	const customers = `{"default_tax_rate":"0","customers":{}}`
	pages := make(map[string]string)
	for _, kind := range []string{"invoice", "estimate"} {
		for _, mode := range []string{"create", "edit"} {
			action := "/" + kind + "s"
			if mode == "edit" {
				action += "/7"
			}
			// This is the production shared editor's keyboard/serialization seam.
			// Full existing-document forms now correctly require real pricing
			// authority; those are covered by persisted handler browser journeys.
			content := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
				if _, err := fmt.Fprintf(w, `<form action="%s" method="post" hx-boost="false" class="compact-form"><input type="hidden" name="customer_id" value="1"><label>Title<input name="title" value="Enter regression" required></label>`, action); err != nil {
					return err
				}
				if err := LineItemEditor(LineItemEditorData{ItemsJSON: `[]`, ExistingItemsJSON: existing, CustomersJSON: customers}).Render(ctx, w); err != nil {
					return err
				}
				_, err := io.WriteString(w, `<button type="submit">Save</button></form>`)
				return err
			})
			page := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
				return Layout("Document line keyboard regression").Render(templ.WithChildren(ctx, content), w)
			})
			pages["/"+kind+"/"+mode] = renderComponent(t, page)
		}
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Accept browser form submissions without involving application persistence.
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if html, ok := pages[r.URL.Path]; ok && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(html))
			return
		}
		http.NotFound(w, r)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/browser/line-items.cjs", server.URL)
	output, err := cmd.CombinedOutput()
	t.Logf("browser output:\n%s", output)
	if err != nil {
		t.Fatalf("document line Enter regression: %v", err)
	}
}
