package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func TestPresentationPreferencesAcrossSessions(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	body := requestBody(t, router, cookie, "GET", "/preferences", 200)
	assertContains(t, body, `data-theme="system"`)
	post := func(values url.Values, want int) {
		t.Helper()
		r := httptest.NewRequest("POST", "/preferences", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("preferences: %d %s", w.Code, w.Body.String())
		}
	}
	post(url.Values{"theme": {"dark"}, "font_size": {"large"}}, http.StatusSeeOther)
	otherDevice := sessionCookie(t, context.Background(), sessions, admin)
	body = requestBody(t, router, otherDevice, "GET", "/preferences", 200)
	assertContains(t, body, `data-theme="dark"`)
	assertContains(t, body, `data-font-size="large"`)
	post(url.Values{"theme": {"invalid"}, "font_size": {"small"}}, 422)
	body = requestBody(t, router, otherDevice, "GET", "/preferences", 200)
	assertContains(t, body, `data-theme="dark"`)
	assertContains(t, body, `data-font-size="large"`)
	// An old browser's cookie cannot replace a subsequently saved explicit choice.
	r := httptest.NewRequest("GET", "/preferences", nil)
	r.AddCookie(otherDevice)
	r.AddCookie(&http.Cookie{Name: "theme", Value: "light"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	assertContains(t, w.Body.String(), `data-theme="dark"`)
}

func TestLegacyThemeImportPreservesExplicitChoices(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	get := func(theme string) string {
		r := httptest.NewRequest("GET", "/preferences", nil)
		r.AddCookie(cookie)
		if theme != "" {
			r.AddCookie(&http.Cookie{Name: "theme", Value: theme})
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("get preferences %d %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	assertContains(t, get("unknown"), `data-theme="system"`)
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM user_presentation`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invented preference: %d %v", count, err)
	}
	assertContains(t, get("light"), `data-theme="light"`)
	assertContains(t, get("dark"), `data-theme="light"`)
	assertContains(t, get(""), `data-theme="light"`)
	var font string
	if err := pool.QueryRow(context.Background(), `SELECT font_size FROM users WHERE id=$1`, admin).Scan(&font); err != nil || font != "medium" {
		t.Fatalf("import changed font: %s %v", font, err)
	}
}

func TestLegacyThemeDoesNotBlockAuthorizationReview(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t, true)
	r := httptest.NewRequest("GET", "/settings/authorization", nil)
	r.AddCookie(sessionCookie(t, context.Background(), sessions, admin))
	r.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("legacy theme blocked cutover: %d %s", w.Code, w.Body.String())
	}
	assertContains(t, w.Body.String(), `data-theme="dark"`)
}

func TestCustomerSaveIdentityAndStaleEditorHTTP(t *testing.T) {
	_, _, router, sessions, _, admin := permissionHTTPFixture(t)
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	getField := func(body, name string) string {
		t.Helper()
		m := regexp.MustCompile(`name="` + name + `" value="([^"]+)"`).FindStringSubmatch(body)
		if len(m) != 2 {
			t.Fatalf("missing %s", name)
		}
		return m[1]
	}
	post := func(path string, values url.Values, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("POST %s: %d want %d %s", path, w.Code, want, w.Body.String())
		}
		return w
	}
	body := requestBody(t, router, cookie, "GET", "/customers/new", 200)
	post("/customers", url.Values{"display_name": {"Unidentified save"}}, 409)
	op := getField(body, "operation_id")
	values := url.Values{"operation_id": {op}, "display_name": {"Exactly once"}}
	w := post("/customers", values, 303)
	edit := w.Header().Get("Location")
	w = post("/customers", values, 303)
	if w.Header().Get("Location") != edit {
		t.Fatal("replay created a different record")
	}
	receipt := requestBody(t, router, cookie, "GET", "/web-actions/"+op, 200)
	assertContains(t, receipt, `"disposition":"saved"`)
	values.Set("display_name", "Changed intent")
	post("/customers", values, 409)
	body = requestBody(t, router, cookie, "GET", edit, 200)
	path := strings.TrimSuffix(edit, "/edit")
	revision := getField(body, "revision")
	first := url.Values{"operation_id": {getField(body, "operation_id")}, "revision": {revision}, "display_name": {"First editor"}}
	secondBody := requestBody(t, router, cookie, "GET", edit, 200)
	second := url.Values{"operation_id": {getField(secondBody, "operation_id")}, "revision": {revision}, "display_name": {"Stale editor"}}
	post(path, first, 303)
	w = post(path, second, 409)
	assertContains(t, w.Body.String(), "First editor")
	assertContains(t, w.Body.String(), "Stale editor")
	assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "First editor")
	invalid := post("/customers", url.Values{"operation_id": {newOperationKey()}, "display_name": {""}, "phone": {"Keep this input"}}, 422)
	assertContains(t, invalid.Body.String(), "Keep this input")
}

func TestWebSaveConcurrencyFailureAndPermissionLoss(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Save test", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.create", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("receipts@test").SetName("Own receipts").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	op := newOperationKey()
	post := func(key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/customers", strings.NewReader(url.Values{"operation_id": {key}, "display_name": {"Sensitive saved name"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		go func() { results <- post(op) }()
	}
	a, b := <-results, <-results
	if a.Code != 303 || b.Code != 303 || a.Header().Get("Location") != b.Header().Get("Location") {
		t.Fatalf("concurrent saves: %d %d %s %s", a.Code, b.Code, a.Body.String(), b.Body.String())
	}
	var effects, audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM customers WHERE display_name='Sensitive saved name'`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='customer.create'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if effects != 1 || audits != 1 {
		t.Fatalf("effects=%d audit=%d", effects, audits)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_browser_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='customer.create' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_browser_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_browser_audit()`); err != nil {
		t.Fatal(err)
	}
	failed := newOperationKey()
	if w := post(failed); w.Code < 400 {
		t.Fatalf("audit failure acknowledged %d", w.Code)
	}
	assertContains(t, requestBody(t, router, cookie, "GET", "/web-actions/"+failed, 200), `"disposition":"unknown"`)
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_browser_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if w := post(failed); w.Code != 303 {
		t.Fatalf("same action retry: %d %s", w.Code, w.Body.String())
	}
	if _, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "No Customer access", nil, 1); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/web-actions/"+op, 200)
	assertContains(t, body, `"disposition":"saved"`)
	assertNotContains(t, body, "Sensitive saved name")
	assertNotContains(t, body, "intent_hash")
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	assertContains(t, requestBody(t, router, adminCookie, "GET", "/web-actions/"+op, 200), `"disposition":"unknown"`)
	expectStatus(t, router, cookie, "GET", strings.TrimSuffix(a.Header().Get("Location"), "/edit"), 404)
	if _, err := pool.Exec(ctx, `UPDATE web_action_receipts SET disposition='invalid' WHERE operation_id=$1`, op); err == nil {
		t.Fatal("receipt is mutable")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM customers WHERE display_name='Sensitive saved name'`).Scan(&effects); err != nil || effects != 2 {
		t.Fatal(fmt.Sprintf("rollback/retry effects=%d: %v", effects, err))
	}
}

func TestAccessibleInteractionPersistedBrowser(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, _, router, sessions, company, admin := permissionHTTPFixture(t)
	if _, err := pool.Exec(context.Background(), `INSERT INTO customers(company_id,display_name) SELECT $1,'Context row '||lpad(i::text,3,'0') FROM generate_series(1,80) i`, company); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	web := nosurf.New(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router))))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.Header.Get("X-Test-Lose-Response") == "true" {
			buffer := httptest.NewRecorder()
			web.ServeHTTP(buffer, r)
			if buffer.Code != 303 {
				t.Errorf("lost response requires committed save, got %d: %s", buffer.Code, buffer.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		web.ServeHTTP(w, r)
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "node", "testdata/browser/interaction.cjs", server.URL, cookie.Value).CombinedOutput()
	t.Logf("browser: %s", output)
	if err != nil {
		t.Fatal(err)
	}
}
