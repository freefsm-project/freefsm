package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func discoveryReset(t *testing.T, f *discoveryFixture, cookie *http.Cookie, revision int64, want int) {
	t.Helper()
	w := discoveryResetResponse(f, cookie, revision)
	if w.Code != want {
		t.Fatalf("reset=%d want=%d: %s", w.Code, want, w.Body.String())
	}
}

func discoveryResetResponse(f *discoveryFixture, cookie *http.Cookie, revision int64) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r := httptest.NewRequest("POST", "/search/reset", strings.NewReader(url.Values{"revision": {fmt.Sprint(revision)}}.Encode()))
	r = r.WithContext(ctx)
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	f.web.ServeHTTP(w, r)
	return w
}

func discoverySave(f *discoveryFixture, cookie *http.Cookie, query string, revision int64) *httptest.ResponseRecorder {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	r := httptest.NewRequest("POST", "/search/state", strings.NewReader(url.Values{"query": {query}, "revision": {fmt.Sprint(revision)}}.Encode()))
	r = r.WithContext(ctx)
	r.AddCookie(cookie)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("X-List-State", "save")
	w := httptest.NewRecorder()
	f.web.ServeHTTP(w, r)
	return w
}

func discoverySaveOK(t *testing.T, f *discoveryFixture, cookie *http.Cookie, query string, revision int64) {
	t.Helper()
	w := discoverySave(f, cookie, query, revision)
	if w.Code != 204 {
		t.Fatalf("save returned %d: %s", w.Code, w.Body.String())
	}
}

func TestDiscoveryServerStateURLResetHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	a := f.client.Item.Create().SetCompanyID(f.company).SetName("Saved106 item").SaveX(ctx)
	b := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Other106 customer").SaveX(ctx)
	domainBefore := historyDurableRows(t, f.pool, []string{"items", "customers"})
	var auditsBefore int
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&auditsBefore))
	f.search(t, f.cookie, "q=Saved106&type=item&per_page=50&sort=name_desc&page=2")
	actor := authorization.Actor{UserID: f.admin}
	svc := authorization.New(f.pool)
	state, err := svc.LoadListState(ctx, actor, "search")
	historyCheck(t, err)
	discoveryEqual(t, state.Revision, int64(0))
	discoverySaveOK(t, f, f.cookie, "q=Saved106&type=item&per_page=50&sort=name_desc&page=2", 0)
	state, err = svc.LoadListState(ctx, actor, "search")
	historyCheck(t, err)
	discoveryEqual(t, state.Values.Get("q"), "Saved106")
	discoveryEqual(t, state.Values.Get("page"), "")
	discoveryEqual(t, historyDurableRows(t, f.pool, []string{"items", "customers"}), domainBefore)
	var auditsAfter int
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&auditsAfter))
	discoveryEqual(t, auditsAfter, auditsBefore)
	fresh := sessionCookie(t, ctx, f.sessions, f.admin)
	o := discoveryObserve(t, requestBody(t, f.web, fresh, "GET", "/search", 200))
	discoveryEqual(t, o.IDs, []string{fmt.Sprintf("item:%d", a.ID)})
	o = f.search(t, fresh, "q=Other106")
	discoveryEqual(t, o.IDs, []string{fmt.Sprintf("customer:%d", b.ID)})
	discoverySaveOK(t, f, fresh, "q=Other106", state.Revision)
	state, err = svc.LoadListState(ctx, actor, "search")
	historyCheck(t, err)
	discoveryEqual(t, state.Values.Get("type"), "")
	discoveryEqual(t, state.Values.Get("per_page"), "25")
	_, _, other := f.reader(t, "OtherState", authorization.Grant{Permission: "item.view", Scope: authorization.All})
	o = discoveryObserve(t, requestBody(t, f.web, other, "GET", "/search", 200))
	discoveryEqual(t, o.IDs, []string{fmt.Sprintf("item:%d", a.ID)})
	discoveryReset(t, f, fresh, state.Revision, 303)
	state, err = svc.LoadListState(ctx, actor, "search")
	historyCheck(t, err)
	discoveryEqual(t, len(state.Values), 0)
	o = discoveryObserve(t, requestBody(t, f.web, fresh, "GET", "/search", 200))
	discoveryEqual(t, o.Total, 2)
	// GET Reset is not a presentation-state mutation.
	state, err = svc.LoadListState(ctx, actor, "search")
	historyCheck(t, err)
	requestBody(t, f.web, fresh, "GET", "/search?reset=1", 200)
	after, err := svc.LoadListState(ctx, actor, "search")
	historyCheck(t, err)
	discoveryEqual(t, after, state)
	f.search(t, fresh, "q=")
	discoverySaveOK(t, f, fresh, "q=", state.Revision)
	state, err = svc.LoadListState(ctx, actor, "search")
	historyCheck(t, err)
	discoveryEqual(t, state.Values.Get("q"), "")
}

func TestDiscoveryMalformedAndRestoredAuthorityHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	i := f.client.Item.Create().SetCompanyID(f.company).SetName("Archived106").SetDeletedAt(time.Now()).SaveX(ctx)
	_, role, cookie := f.reader(t, "ArchivedState", authorization.Grant{Permission: "item.view", Scope: authorization.All}, authorization.Grant{Permission: "item.archived.view", Scope: authorization.All})
	discoveryEqual(t, f.search(t, cookie, "q=Archived106&type=item").Total, 0)
	o := f.search(t, cookie, "q=Archived106&type=item&state=archived")
	discoverySaveOK(t, f, cookie, "q=Archived106&type=item&state=archived", 0)
	discoveryEqual(t, o.IDs, []string{fmt.Sprintf("item:%d", i.ID)})
	if !strings.Contains(strings.Join(o.Text, " "), "Archived") {
		t.Fatal("missing archived badge")
	}
	_, err := authorization.New(f.pool).SaveRole(ctx, authorization.Actor{UserID: f.admin}, role, "ArchivedState", []authorization.Grant{{Permission: "item.view", Scope: authorization.All}}, 1)
	historyCheck(t, err)
	for _, path := range []string{"/search", "/search?q=Archived106&type=item&state=archived"} {
		body := requestBody(t, f.web, cookie, "GET", path, 403)
		assertNotContains(t, body, `data-record-id=`)
		assertContains(t, body, "Reset")
	}
	discoveryEqual(t, discoverySave(f, cookie, "q=Archived106&type=item&state=archived", 1).Code, 403)
	for _, query := range []string{
		"q=x&unknown=x", "q=x&q=y", "page=0", "page=1000001", "page=wat", "per_page=26", "sort=bogus",
		"state=bogus", "type=bogus", "type=item&type=item", "reset=1&q=x", "q=" + strings.Repeat("x", 501),
		"type=lease&ending_through=2026-02-30", "type=lease&customer=-1",
	} {
		t.Run(query[:min(len(query), 60)], func(t *testing.T) {
			body := requestBody(t, f.web, f.cookie, "GET", "/search?"+query, 400)
			assertNotContains(t, body, `data-record-id=`)
		})
	}
}

func TestDiscoveryWholeSearchFailureHTTP(t *testing.T) {
	for _, table := range []string{"items", "custom_field_definitions", "list_states"} {
		t.Run(table, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			ctx := context.Background()
			f.client.Item.Create().SetCompanyID(f.company).SetName("Failure106 item").SaveX(ctx)
			f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Failure106 customer").SaveX(ctx)
			f.search(t, f.cookie, "q=Failure106")
			var before int
			historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&before))
			_, err := f.pool.Exec(ctx, `ALTER TABLE `+table+` RENAME TO discovery_unavailable`)
			historyCheck(t, err)
			t.Cleanup(func() {
				_, err := f.pool.Exec(context.Background(), `ALTER TABLE discovery_unavailable RENAME TO `+table)
				if err != nil {
					t.Error(err)
				}
			})
			body := requestBody(t, f.web, f.cookie, "GET", "/search?q=Failure106", 503)
			for _, secret := range []string{"Failure106 item", "Failure106 customer", "SQLSTATE", "does not exist", "data-record-id="} {
				assertNotContains(t, body, secret)
			}
			assertContains(t, body, "Failure106")
			assertContains(t, body, "Retry")
			var after int
			historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit`).Scan(&after))
			discoveryEqual(t, after, before)
		})
	}
}

func TestDiscoveryPreferenceWriteFailureIsWholeFailureHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx := context.Background()
	f.client.Item.Create().SetCompanyID(f.company).SetName("WriteFailure106").SaveX(ctx)
	_, err := f.pool.Exec(ctx, `CREATE FUNCTION discovery_reject_state() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'private storage diagnostic106'; END $$;
		CREATE TRIGGER discovery_state_failure BEFORE INSERT OR UPDATE ON list_states FOR EACH ROW EXECUTE FUNCTION discovery_reject_state()`)
	historyCheck(t, err)
	w := discoverySave(f, f.cookie, "q=WriteFailure106", 0)
	discoveryEqual(t, w.Code, 503)
	body := w.Body.String()
	assertNotContains(t, body, "private storage diagnostic106")
	assertNotContains(t, body, "data-record-id=")
	assertContains(t, body, "retry")
	var count int
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM list_states`).Scan(&count))
	discoveryEqual(t, count, 0)
}

func TestDiscoveryQueuedSaveCannotResurrectResetHTTP(t *testing.T) {
	f := newDiscoveryFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f.client.Item.Create().SetCompanyID(f.company).SetName("Race106 item").SaveX(ctx)
	f.search(t, f.cookie, "q=Race106&type=item")
	discoverySaveOK(t, f, f.cookie, "q=Race106&type=item", 0)
	svc := authorization.New(f.pool)
	actor := authorization.Actor{UserID: f.admin}
	state, err := svc.LoadListState(ctx, actor, "search")
	historyCheck(t, err)
	tx, err := f.pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(context.Background())
	var blocker int
	historyCheck(t, tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&blocker))
	reset := make(chan *httptest.ResponseRecorder, 1)
	go func() { reset <- discoveryResetResponse(f, f.cookie, state.Revision) }()
	discoveryWaitBlocked(t, ctx, f.pool, blocker, 1)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- discoverySave(f, f.cookie, "q=Race106&type=item&sort=name_desc", state.Revision) }()
	discoveryWaitBlocked(t, ctx, f.pool, blocker, 2)
	historyCheck(t, tx.Commit(ctx))
	select {
	case w := <-reset:
		discoveryEqual(t, w.Code, 303)
	case <-ctx.Done():
		t.Fatal("queued Reset did not finish")
	}
	select {
	case response := <-done:
		discoveryEqual(t, response.Code, 409)
		assertNotContains(t, response.Body.String(), "data-record-id=")
	case <-ctx.Done():
		t.Fatal("in-flight search did not finish")
	}
	after, err := svc.LoadListState(ctx, actor, "search")
	historyCheck(t, err)
	discoveryEqual(t, len(after.Values), 0)
	discoveryEqual(t, after.Revision, state.Revision+1)
}
