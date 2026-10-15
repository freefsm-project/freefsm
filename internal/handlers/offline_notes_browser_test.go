package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestOfflineNotesPolicyPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", nosurf.New(middleware.CSRFToken(router)))
	server := httptest.NewServer(mux)
	defer server.Close()
	input, _ := json.Marshal(map[string]string{"url": server.URL, "cookie": cookie.Value})
	runCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	command := exec.CommandContext(runCtx, "node", "testdata/browser/offline-notes-policy.cjs")
	command.Stdin = bytes.NewReader(input)
	out, err := command.CombinedOutput()
	t.Logf("offline policy browser: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var hours int
	if err = pool.QueryRow(ctx, `SELECT offline_hours FROM company_settings WHERE company_id=$1`, company).Scan(&hours); err != nil {
		t.Fatal(err)
	}
	if hours != 0 {
		t.Fatalf("browser disable did not persist: %d", hours)
	}
}
