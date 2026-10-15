package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestTimeTransientRetryPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("before browser dependency fault").SaveX(ctx)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION browser_time_history_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected history unavailable'; END $$; CREATE TRIGGER browser_time_history_fault BEFORE INSERT ON time_entry_history FOR EACH ROW EXECUTE FUNCTION browser_time_history_fault()`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.HandleFunc("/__restore_history", func(w http.ResponseWriter, r *http.Request) {
		if _, err := pool.Exec(ctx, `DROP TRIGGER browser_time_history_fault ON time_entry_history`); err != nil {
			http.Error(w, "fixture recovery failed", 500)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router)))))
	server := httptest.NewServer(mux)
	defer server.Close()
	output, err := exec.Command("node", "testdata/browser/time-retry.cjs", server.URL, sessionCookie(t, ctx, sessions, admin).Value, fmt.Sprint(entry.ID)).CombinedOutput()
	t.Log(string(output))
	if err != nil {
		t.Fatal(err)
	}
}
