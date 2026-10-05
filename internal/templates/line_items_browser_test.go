package templates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Opt-in so the ordinary Go suite does not require Node or a browser.
func TestDocumentLineEnterBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("see testdata/browser/README.md to run the real-browser regression")
	}
	const existing = `[{"item_id":0,"title":"First distinct line","description":"First description","quantity":2,"unit_price":11,"discount":1,"surcharge":2,"taxable":false,"tax_rate":""},{"item_id":0,"title":"Second distinct line","description":"Second description","quantity":3,"unit_price":23,"discount":2,"surcharge":1,"taxable":false,"tax_rate":""}]`
	const customers = `{"default_tax_rate":"0","customers":{}}`
	options := []SelectOption{{Value: 1, Label: "Regression customer"}}
	pages := make(map[string]string)
	for _, mode := range []string{"create", "edit"} {
		pages["/invoice/"+mode] = renderComponent(t, InvoiceForm(InvoiceFormPageData{IsNew: mode == "create", Invoice: &InvoiceDetail{ID: 7, Number: 42, CustomerID: 1, Title: "Enter regression"}, Customers: options, ItemsJSON: `[]`, ExistingItemsJSON: existing, CustomersJSON: customers}))
		pages["/estimate/"+mode] = renderComponent(t, EstimateForm(EstimateFormPageData{IsNew: mode == "create", Estimate: &EstimateDetail{ID: 7, CustomerID: 1, Title: "Enter regression"}, Customers: options, ItemsJSON: `[]`, ExistingItemsJSON: existing, CustomersJSON: customers}))
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
