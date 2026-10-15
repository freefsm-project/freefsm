package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/database"
	"io/fs"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"
)

func TestTimeInstalledHistoryRequiresExplicitReviewHTTP(t *testing.T) {
	source := database.MigrationFS()
	old := fstest.MapFS{}
	files, err := fs.ReadDir(source, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "085_time_entry_history") {
			continue
		}
		data, err := fs.ReadFile(source, f.Name())
		if err != nil {
			t.Fatal(err)
		}
		old[f.Name()] = &fstest.MapFile{Data: data}
	}
	pool, _, router, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	var first, second int64
	if err := pool.QueryRow(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in,clock_out,notes,latitude) VALUES($1,$2,'2026-01-01T10:00Z','2026-01-01T11:00Z','original installed facts',51.234567) RETURNING id`, company, admin).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO time_entries(company_id,user_id,clock_in,clock_out,notes) VALUES($1,$2,'2026-01-01T10:30Z','2026-01-01T12:00Z','overlapping installed facts') RETURNING id`, company, admin).Scan(&second); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		var v string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_object('entries',(SELECT jsonb_agg(to_jsonb(e)-'voided_at'-'job_title_snapshot' ORDER BY id) FROM time_entries e),'grants',(SELECT jsonb_agg(to_jsonb(g) ORDER BY role_id,permission,scope) FROM authorization_grants g))::text`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	before := snapshot()
	for range 2 {
		if err := (&database.DB{Pool: pool}).Migrate(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("upgrade rewrote installed facts/grants:\n%s\n%s", before, after)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	body := requestBody(t, router, cookie, "GET", fmt.Sprintf("/time-entries/%d", second), 200)
	assertContains(t, body, "Recorded interval requires review")
	assertContains(t, body, "retained_at_upgrade")
	firstBody := requestBody(t, router, cookie, "GET", fmt.Sprintf("/time-entries/%d", first), 200)
	assertContains(t, firstBody, "capture time not recorded")
	timePost(t, router, cookie, fmt.Sprintf("/time-entries/%d", second), url.Values{"clock_in": {"2026-01-01T11:00:00Z"}, "reason": {"Administrator reviewed original paper records"}}, 303)
	body = requestBody(t, router, cookie, "GET", fmt.Sprintf("/time-entries/%d", second), 200)
	assertNotContains(t, body, "Recorded interval requires review")
	assertContains(t, body, "2026-01-01T10:30:00Z")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM time_entry_history WHERE event='retained_at_upgrade'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("baseline count=%d err=%v", count, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE time_entry_history SET reason='rewrite'`); err == nil {
		t.Fatal("history mutable")
	}
}
