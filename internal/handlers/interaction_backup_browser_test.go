package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestBackupInitiationPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_BACKUP_TEST_ADMIN_URL for the real engine fixture")
	}
	backupHTTPRoundTrip(t, "guarded-initiation-browser")
}

func runBackupInitiationBrowser(t *testing.T, router http.Handler, cookie string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", router)
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/backup-initiation.cjs", server.URL, cookie).CombinedOutput()
	t.Logf("real backup browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
