package templates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent"
)

// Complements the real-PostgreSQL handler test with browser transfer semantics,
// production HTMX, and execution of the actual inline backup UI script.
func TestBackupBrowserWorkflow(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 with Chromium and testdata/browser dependencies")
	}
	page := renderComponent(t, BackupPage("Destination", "", false))
	settings := renderComponent(t, SettingsPage(SettingsPageData{Settings: &ent.CompanySettings{BusinessName: "Destination"}, EmailDisabled: true}))
	setup := renderComponent(t, SettingsPage(SettingsPageData{Settings: &ent.CompanySettings{}, IsSetup: true}))
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/settings" {
			_, _ = w.Write([]byte(settings))
			return
		}
		if r.URL.Path == "/setup/company" {
			_, _ = w.Write([]byte(setup))
			return
		}
		if r.URL.Path == "/settings/activity" {
			return
		}
		if r.URL.Path == "/settings/backup/status-page" {
			_ = BackupStatusPage("browser-fixture").Render(r.Context(), w)
			return
		}
		if r.URL.Path == "/elsewhere" {
			_, _ = w.Write([]byte("<html><body>Another page</body></html>"))
			return
		}
		_, _ = w.Write([]byte(page))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/backup.cjs", server.URL).CombinedOutput()
	t.Logf("browser output: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
