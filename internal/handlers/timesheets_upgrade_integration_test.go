package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/database"
)

func TestTimesheetsRetainedReversedVoidHistoryHTTP(t *testing.T) {
	source := database.MigrationFS()
	old := fstest.MapFS{}
	files, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "085_time_entry_history") {
			continue
		}
		data, err := fs.ReadFile(source, file.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[file.Name()] = &fstest.MapFile{Data: data}
	}
	pool, _, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET timezone='UTC'`); err != nil {
		t.Fatal(err)
	}
	var reversed int64
	if err := pool.QueryRow(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in,clock_out,notes) VALUES($1,$2,'2026-01-02T10:00Z','2026-01-01T09:00Z','Retained reversed report facts') RETURNING id`, company, admin).Scan(&reversed); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in,clock_out,notes) VALUES($1,$2,'2026-01-02T11:00Z','2026-01-02T12:00Z','Valid report hour')`, company, admin); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := (&database.DB{Pool: pool}).Migrate(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/time-entries/%d", reversed)
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Recorded interval requires review")
	timePost(t, router, cookie, path+"/void", url.Values{"reason": {"Reviewed original reversed interval; retain terminal history"}}, 303)
	assertContains(t, requestBody(t, router, cookie, "GET", "/time-entries", 200), `data-timesheet-total>1h0m0s</strong>`)
	for _, query := range []string{"include_void=true", "include_void=true&date_from=2026-01-02&date_to=2026-01-02"} {
		body := requestBody(t, router, cookie, "GET", "/time-entries?"+query, 200)
		for _, value := range []string{"Retained reversed report facts", "Reviewed original reversed interval; retain terminal history", "retained_at_upgrade", "Unknown historical actor", "2026-01-02T10:00:00Z", "2026-01-01T09:00:00Z", "Excluded (Void)", `data-timesheet-total>1h0m0s</strong>`} {
			assertContains(t, body, value)
		}
	}
	body := requestBody(t, router, cookie, "GET", "/time-entries?include_void=true&lifecycle=void", 200)
	assertContains(t, body, "Retained reversed report facts")
	assertContains(t, body, `data-timesheet-total>0s</strong>`)
	assertNotContains(t, body, "Valid report hour")
	// No elapsed range can be inferred from reversed endpoints. Selection uses
	// the retained start's company date, without sorting/swapping the instants.
	body = requestBody(t, router, cookie, "GET", "/time-entries?include_void=true&date_from=2026-01-01&date_to=2026-01-01", 200)
	assertNotContains(t, body, "Retained reversed report facts")
	assertContains(t, body, `data-timesheet-total>0s</strong>`)
	detail := requestBody(t, router, cookie, "GET", path, 200)
	assertContains(t, detail, "2026-01-02T10:00:00Z")
	assertContains(t, detail, "2026-01-01T09:00:00Z")
}
