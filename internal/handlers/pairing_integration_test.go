package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/google/uuid"
)

func approvedPairing(t *testing.T, api, web http.Handler, cookie *http.Cookie) string {
	t.Helper()
	w := pairingCall(api, "/pairings", `{"device_name":"Protocol phone","operation_id":"`+uuid.NewString()+`"}`)
	if w.Code != 201 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		Data struct {
			DeviceCode string `json:"device_code"`
			UserCode   string `json:"user_code"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	review := pairingBrowserPost(web, cookie, "/pair/review", url.Values{"code": {out.Data.UserCode}})
	match := regexp.MustCompile(`name="review_token" value="([^"]+)"`).FindStringSubmatch(review.Body.String())
	if review.Code != 200 || len(match) != 2 {
		t.Fatalf("review: %d %s", review.Code, review.Body.String())
	}
	approve := pairingBrowserPost(web, cookie, "/pair/approve", url.Values{"review_token": {match[1]}, "device_name": {"Protocol phone"}})
	if approve.Code != 200 {
		t.Fatalf("approve: %d %s", approve.Code, approve.Body.String())
	}
	return `{"device_code":"` + out.Data.DeviceCode + `","operation_id":"` + uuid.NewString() + `"}`
}
func pairingBrowserPost(web http.Handler, cookie *http.Cookie, path string, values url.Values) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	web.ServeHTTP(w, r)
	return w
}

func pairingCall(api http.Handler, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	return w
}

func TestPairingBrowserApprovalHTTP(t *testing.T) {
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	api := apiv1.NewRouter(pool, client, sessions)
	w := pairingCall(api, "/pairings", `{"device_name":"Field tablet","operation_id":"e6b77bc3-1ab4-42bc-9750-62e430a7a351"}`)
	if w.Code != 201 {
		t.Fatalf("start: %d %s", w.Code, w.Body.String())
	}
	var start struct {
		Data struct {
			DeviceCode string `json:"device_code"`
			UserCode   string `json:"user_code"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	if start.Data.DeviceCode == "" || start.Data.UserCode == "" || start.Data.DeviceCode == start.Data.UserCode {
		t.Fatal("distinct secrets required")
	}
	body := `{"device_code":"` + start.Data.DeviceCode + `","operation_id":"e6b77bc3-1ab4-42bc-9750-62e430a7a352"}`
	if w := pairingCall(api, "/pairings/redeem", body); w.Code != 409 {
		t.Fatalf("unapproved redeemed: %d", w.Code)
	}
	cookie := sessionCookie(t, context.Background(), sessions, admin)
	post := func(path string, v url.Values) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", path, strings.NewReader(v.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		return w
	}
	w = post("/pair/review", url.Values{"code": {start.Data.UserCode}})
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Field tablet") {
		t.Fatalf("review: %d %s", w.Code, w.Body.String())
	}
	match := regexp.MustCompile(`name="review_token" value="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 {
		t.Fatal("missing bound review")
	}
	w = post("/pair/approve", url.Values{"review_token": {match[1]}, "device_name": {"Wrong tablet"}})
	if w.Code < 400 {
		t.Fatal("wrong displayed device approved")
	}
	w = post("/pair/approve", url.Values{"review_token": {match[1]}, "device_name": {"Field tablet"}})
	if w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body.String())
	}
	w = pairingCall(api, "/pairings/redeem", body)
	if w.Code != 201 {
		t.Fatalf("redeem: %d %s", w.Code, w.Body.String())
	}
	var redeemed struct {
		Data struct {
			Token string `json:"token"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &redeemed); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/sessions", nil)
	r.Header.Set("Authorization", "Bearer "+redeemed.Data.Token)
	w = httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Field tablet") {
		t.Fatalf("own sessions: %d %s", w.Code, w.Body.String())
	}
	if w := pairingCall(api, "/pairings/redeem", body); w.Code != 409 {
		t.Fatalf("replay: %d", w.Code)
	}
	var events int
	err := pool.QueryRow(context.Background(), `SELECT count(*) FROM authorization_audit a JOIN sessions s ON s.id=a.target_id JOIN users u ON u.id=a.actor_id JOIN authorization_roles role ON role.id=a.role_id
 WHERE a.action='user.pairing.redeem' AND a.actor_id=$1 AND a.role_id=u.authorization_role_id AND a.channel='api' AND a.scope='O'
 AND a.detail->>'session_id'=s.id::text AND a.detail->>'device_name'='Field tablet'
 AND a.detail->>'operation_id'='e6b77bc3-1ab4-42bc-9750-62e430a7a352' AND s.kind='mobile'
 AND a.session_id=s.id AND a.device_name='Field tablet' AND a.operation_id='e6b77bc3-1ab4-42bc-9750-62e430a7a352'
 AND a.actor_identity=u.audit_identity AND a.actor_name=u.name AND a.role_identity=role.audit_identity AND a.role_name=role.name
 AND NOT (a.detail::text LIKE '%' || s.token_hash || '%') AND NOT (a.detail::text LIKE '%' || $2 || '%')`, admin, redeemed.Data.Token).Scan(&events)
	if err != nil || events != 1 {
		t.Fatalf("issued mobile session attribution: count=%d err=%v", events, err)
	}
}

func TestPairingPublicBoundaryHTTP(t *testing.T) {
	pool, client, _, sessions, _, _ := permissionHTTPFixture(t)
	api := apiv1.NewRouter(pool, client, sessions)
	t.Run("password login retired", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/session", strings.NewReader(`{"email":"admin@test","password":"secret"}`))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 410 || !strings.Contains(w.Body.String(), `"pairing_required"`) {
			t.Fatalf("password route: %d %s", w.Code, w.Body.String())
		}
	})
	t.Run("minimal discovery", func(t *testing.T) {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"api_major":1`) {
			t.Fatalf("discovery: %d %s", w.Code, w.Body.String())
		}
		if w.Header().Get("X-FreeFSM-Session-Epoch") != "" {
			t.Fatal("public epoch disclosure")
		}
		for _, secret := range []string{"company", "tenant", "version", "grants", "limits", "epoch", "admin"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatalf("public discovery disclosed %s", secret)
			}
		}
	})
	t.Run("private denial has no epoch", func(t *testing.T) {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("GET", "/me", nil))
		if w.Code != 401 || w.Header().Get("X-FreeFSM-Session-Epoch") != "" {
			t.Fatal("unauthenticated private route disclosed epoch")
		}
	})
	t.Run("discovery rejects unknown filters", func(t *testing.T) {
		w := httptest.NewRecorder()
		api.ServeHTTP(w, httptest.NewRequest("GET", "/?tenant=secret", nil))
		if w.Code != 400 {
			t.Fatalf("unknown filter=%d", w.Code)
		}
	})
}

func TestPairingStartAuditFailureHTTP(t *testing.T) {
	pool, client, _, sessions, _, _ := permissionHTTPFixture(t)
	ctx := context.Background()
	api := apiv1.NewRouter(pool, client, sessions)
	_, err := pool.Exec(ctx, `CREATE FUNCTION reject_pair_start() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user.pairing.start' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_pair_start BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_pair_start()`)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"device_name":"Atomic start","operation_id":"` + uuid.NewString() + `"}`
	if w := pairingCall(api, "/pairings", body); w.Code != 503 {
		t.Fatalf("audit failure acknowledged: %d", w.Code)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_pair_start ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if w := pairingCall(api, "/pairings", body); w.Code != 201 {
		t.Fatalf("failed start consumed operation: %d", w.Code)
	}
}
