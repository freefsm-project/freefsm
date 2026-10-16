package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
)

func safetyRequest(router http.Handler, cookie *http.Cookie, method, path string, values url.Values, history string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if history != "" {
		r.Header.Set("X-FreeFSM-History", history)
	}
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestSaveSafetyAuthenticationDependencyHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, table := range []string{"sessions", "users", "authorization_state", "authorization_grants"} {
		t.Run(table, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `ALTER TABLE `+table+` RENAME TO safety_unavailable`); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := pool.Exec(ctx, `ALTER TABLE safety_unavailable RENAME TO `+table); err != nil {
					t.Fatal(err)
				}
			}()
			for _, method := range []string{"GET", "POST"} {
				id := newOperationKey()
				w := safetyRequest(router, cookie, method, "/preferences", url.Values{"save_action_id": {id}, "theme": {"dark"}, "font_size": {"large"}}, "")
				if w.Code != 503 || !strings.Contains(strings.ToLower(w.Body.String()), "retry") {
					t.Errorf("%s dependency failure: %d %s", method, w.Code, w.Body.String())
				}
				for _, c := range w.Result().Cookies() {
					if c.Name == "session" {
						t.Errorf("dependency failure changed session cookie: %v", c)
					}
				}
				assertNotContains(t, w.Body.String(), "SQLSTATE")
				var count int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM online_save_actions WHERE operation_id=$1`, id).Scan(&count); err != nil || count != 0 {
					t.Fatalf("dispatched during dependency failure: %d %v", count, err)
				}
			}
		})
		requestBody(t, router, cookie, "GET", "/preferences", 200)
	}
	for _, condition := range []string{"expires_at=now()-interval '1 second'", "revoked_at=now()"} {
		denied := sessionCookie(t, ctx, sessions, admin)
		sum := sha256.Sum256([]byte(denied.Value))
		if _, err := pool.Exec(ctx, `UPDATE sessions SET `+condition+` WHERE token_hash=$1`, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
		w := safetyRequest(router, denied, "POST", "/preferences", nil, "")
		if w.Code != 401 || !strings.Contains(w.Header().Get("Set-Cookie"), "session=;") {
			t.Fatalf("real expired/revoked credential not denied: %d %v", w.Code, w.Header())
		}
	}
}

func TestSaveSafetyNativeInitialReadFailureHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, table := range []string{"customers", "items", "jobs", "projects", "assets", "estimates", "invoices", "time_entries"} {
		t.Run(table, func(t *testing.T) {
			if _, err := pool.Exec(ctx, `ALTER TABLE `+table+` RENAME TO safety_unavailable_records`); err != nil {
				t.Fatal(err)
			}
			defer pool.Exec(ctx, `ALTER TABLE safety_unavailable_records RENAME TO `+table)
			sort := "name_desc"
			if table == "time_entries" {
				// Exercise the read fault with a valid report selector, rather
				// than an unrelated validation rejection before the database read.
				sort = "start_desc"
			}
			path := "/" + strings.ReplaceAll(table, "_", "-") + "?search=Keep+filter&sort=" + sort + "&page=2"
			w := safetyRequest(router, cookie, "GET", path, nil, "")
			if w.Code != 503 || !strings.Contains(w.Body.String(), "Retry") {
				t.Fatalf("native initial read failure: %d %.300s", w.Code, w.Body.String())
			}
			assertContains(t, w.Body.String(), "search=Keep+filter")
			for _, leak := range []string{"SQLSTATE", "does not exist", "No customers", "No matches"} {
				assertNotContains(t, w.Body.String(), leak)
			}
		})
	}
}

func TestSaveSafetyCommitPrincipalDependencyHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	// Authentication has completed when journal admission runs. Fail the real
	// subsequent commit-time Principal's session query, not the outer auth check.
	_, err := pool.Exec(ctx, `CREATE FUNCTION safety_break_session_query() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN ALTER TABLE sessions RENAME TO safety_sessions; RETURN NEW; END $$; CREATE TRIGGER safety_break_session_query BEFORE INSERT ON online_save_actions FOR EACH ROW EXECUTE FUNCTION safety_break_session_query()`)
	if err != nil {
		t.Fatal(err)
	}
	id := newOperationKey()
	w := safetyRequest(router, cookie, "POST", "/preferences", url.Values{"save_action_id": {id}, "theme": {"dark"}, "font_size": {"large"}}, "")
	if _, err := pool.Exec(ctx, `ALTER TABLE safety_sessions RENAME TO sessions; DROP TRIGGER safety_break_session_query ON online_save_actions`); err != nil {
		t.Fatal(err)
	}
	if w.Code != 503 || !strings.Contains(w.Body.String(), "Retry") {
		t.Fatalf("Principal dependency presented as denial/conflict: %d %s", w.Code, w.Body.String())
	}
	assertNotContains(t, requestBody(t, router, cookie, "GET", "/preferences", 200), `data-theme="dark"`)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM online_save_effects WHERE operation_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("dependency failure committed effects: %d %v", count, err)
	}
}

func TestSaveSafetyKeyedCredentialIntentRotationHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	router := func(secret string) http.Handler {
		return middleware.Company(services.NewCompanySettingsService(client))(New(pool, client, sessions, &config.Config{SessionSecret: secret, UploadDir: t.TempDir()}))
	}
	a := router("operator-held-safety-test-key-A")
	b := router("operator-held-safety-test-key-B")
	origin := safetyRequest(a, cookie, "GET", "/preferences", nil, "").Header().Get("X-FreeFSM-History")
	rotatedMissing := safetyRequest(b, cookie, "GET", "/save-actions/"+newOperationKey(), nil, origin)
	if rotatedMissing.Code != 200 || !strings.Contains(rotatedMissing.Body.String(), `"history_changed":true`) {
		t.Fatalf("rotated history misclassified absent receipt: %d %s", rotatedMissing.Code, rotatedMissing.Body.String())
	}
	values := url.Values{"current_password": {"incorrect-secret"}, "new_password": {"ProposedSecret123!"}, "confirm_password": {"ProposedSecret123!"}, "save_action_id": {newOperationKey()}}
	w := safetyRequest(a, cookie, "POST", "/change-password", values, "")
	if w.Code != 303 {
		t.Fatalf("credential intent: %d %s", w.Code, w.Body.String())
	}
	id := values.Get("save_action_id")
	var fingerprint string
	if err := pool.QueryRow(ctx, `SELECT intent_hash FROM online_save_actions WHERE operation_id=$1`, id).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	canonical := url.Values{}
	for k, v := range values {
		if k != "save_action_id" {
			canonical[k] = v
		}
	}
	unkeyed := sha256.Sum256([]byte("POST\n/change-password\napplication/x-www-form-urlencoded\n\n" + canonical.Encode()))
	if fingerprint == hex.EncodeToString(unkeyed[:]) {
		t.Fatal("persisted credential intent is a cheap unkeyed password verifier")
	}
	// A router restart under the same key suppresses the exact same request.
	w = safetyRequest(router("operator-held-safety-test-key-A"), cookie, "POST", "/change-password", values, "")
	if w.Code != 409 || strings.Contains(w.Body.String(), "identity_conflict") {
		t.Fatalf("same-key canonical intent: %d %s", w.Code, w.Body.String())
	}
	w = safetyRequest(b, cookie, "POST", "/change-password", values, "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "identity_conflict") {
		t.Fatalf("rotation must fail closed: %d %s", w.Code, w.Body.String())
	}
	body := requestBody(t, b, cookie, "GET", "/save-actions/"+id, 200)
	assertNotContains(t, body, "identity_conflict")
	for _, secret := range []string{"incorrect-secret", "ProposedSecret123!", "operator-held", fingerprint} {
		assertNotContains(t, body, secret)
	}
	values.Set("save_action_id", newOperationKey())
	w = safetyRequest(b, cookie, "POST", "/change-password", values, "")
	if w.Code != 303 {
		t.Fatalf("independent intent under new key: %d %s", w.Code, w.Body.String())
	}
	var otherFingerprint, stored string
	if err := pool.QueryRow(ctx, `SELECT intent_hash,row_to_json(a)::text FROM online_save_actions a WHERE operation_id=$1`, values.Get("save_action_id")).Scan(&otherFingerprint, &stored); err != nil {
		t.Fatal(err)
	}
	if otherFingerprint == fingerprint {
		t.Fatal("different operator keys produced the same credential verifier")
	}
	for _, secret := range []string{"incorrect-secret", "ProposedSecret123!", "operator-held"} {
		assertNotContains(t, stored, secret)
	}
}

func TestSaveSafetyReceiptDependencyHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	id := newOperationKey()
	w := safetyRequest(router, cookie, "POST", "/preferences", url.Values{"save_action_id": {id}, "theme": {"dark"}, "font_size": {"large"}}, "")
	if w.Code != 303 {
		t.Fatalf("save fixture: %d %s", w.Code, w.Body.String())
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE online_save_actions RENAME TO safety_unavailable_receipts`); err != nil {
		t.Fatal(err)
	}
	defer pool.Exec(ctx, `ALTER TABLE safety_unavailable_receipts RENAME TO online_save_actions`)
	for _, suffix := range []string{"", "/input-access"} {
		w := safetyRequest(router, cookie, "GET", "/save-actions/"+id+suffix, nil, "")
		if w.Code != 503 {
			t.Errorf("receipt dependency pretends absent/revoked: %s: %d %s", suffix, w.Code, w.Body.String())
		}
	}
}

func TestSaveSafetyMissingReceiptAuthenticationHistoryHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	page := safetyRequest(router, cookie, "GET", "/preferences", nil, "")
	history := page.Header().Get("X-FreeFSM-History")
	if len(history) != 64 || history == cookie.Value {
		t.Fatalf("missing noncredential history tag: %q", history)
	}
	assertContains(t, page.Body.String(), `name="freefsm-save-history" content="`+history+`"`)
	assertContains(t, page.Body.String(), `name="save_history" value="`+history+`"`)
	id := newOperationKey()
	path := "/save-actions/" + id
	read := func(c *http.Cookie, tag string, disposition string, changed bool) {
		t.Helper()
		w := safetyRequest(router, c, "GET", path, nil, tag)
		var receipt struct {
			Disposition    string `json:"disposition"`
			HistoryChanged bool   `json:"history_changed"`
			RetrySafe      bool   `json:"retry_safe"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || receipt.Disposition != disposition || receipt.HistoryChanged != changed || (changed && receipt.RetrySafe) {
			t.Fatalf("history receipt: %d %s", w.Code, w.Body.String())
		}
	}
	read(cookie, history, "not_received", false)
	read(cookie, "", "review", true)
	// Restore's session revocation is covered by the actual backup HTTP round-trip;
	// this composes its postcondition with a genuine newly issued web session.
	if err := sessions.Delete(ctx, cookie.Value); err != nil {
		t.Fatal(err)
	}
	fresh := sessionCookie(t, ctx, sessions, admin)
	read(fresh, history, "review", true)
	values := url.Values{"save_action_id": {id}, "save_history": {history}, "theme": {"dark"}, "font_size": {"large"}}
	w := safetyRequest(router, fresh, "POST", "/preferences", values, "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), `"history_changed":true`) {
		t.Fatalf("old history was dispatched: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM online_save_actions WHERE operation_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal(fmt.Sprintf("history mismatch admitted: %d %v", count, err))
	}
	assertContains(t, requestBody(t, router, fresh, "GET", path+"/review", 200), "may have completed")
	// The same-session not-received path remains usable with the same identity.
	freshHistory := safetyRequest(router, fresh, "GET", "/preferences", nil, "").Header().Get("X-FreeFSM-History")
	values.Set("save_action_id", newOperationKey())
	values.Set("save_history", freshHistory)
	w = safetyRequest(router, fresh, "POST", "/preferences", values, "")
	if w.Code != 303 {
		t.Fatalf("current history not admitted: %d %s", w.Code, w.Body.String())
	}
	path = "/save-actions/" + values.Get("save_action_id")
	read(sessionCookie(t, ctx, sessions, admin), history, "saved", false)
}
