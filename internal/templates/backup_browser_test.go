package templates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/backup"
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
	var activityLoads atomic.Int64
	activity := func() ActivityPageData {
		return ActivityPageData{Entries: []ActivityEntry{{ActorName: fmt.Sprintf("Historical administrator %d", activityLoads.Load()), Action: "backup_created", TargetType: "instance", EntityName: "Instance backup and restore", EntityURL: "/settings/backup", CreatedAt: "2026-10-05 12:00"}}, HasMore: true, ViewAllURL: "/activity?type=instance"}
	}
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
		if r.URL.Path == "/settings/backup/activity" {
			activityLoads.Add(1)
			_ = ActivityRecentList(activity()).Render(r.Context(), w)
			return
		}
		if r.URL.Path == "/activity" {
			if r.URL.Query().Get("type") != "instance" {
				http.Error(w, "instance filter required", http.StatusBadRequest)
				return
			}
			_ = ActivityIndex(activity()).Render(r.Context(), w)
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
	// Marshal the production type: in particular, a successful Operation includes
	// a non-null Failure object whose exported fields are all empty strings.
	operationJSON, err := json.Marshal(backup.Operation{
		ID: "secret-capability", Kind: "backup", Phase: "complete",
		SourceName: "Source <script>unsafe</script>", BuildKind: backup.BuildDevelopment,
		Version: "dev", Commit: "none", ArchiveDigest: "review-digest",
		CapturedAt: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/backup.cjs", server.URL, string(operationJSON)).CombinedOutput()
	t.Logf("browser output: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
