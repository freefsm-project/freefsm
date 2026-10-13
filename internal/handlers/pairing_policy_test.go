package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/requestlimits"
	"github.com/go-chi/chi/v5"
)

func TestPairingConfiguredBudgetsAndCORSHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	first, err := sessions.CreateMobile(ctx, admin, "Budget one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := sessions.CreateMobile(ctx, admin, "Budget two")
	if err != nil {
		t.Fatal(err)
	}
	policy := config.APIConfig{RequestsPerUser: 3, SyncPerClient: 1, CORSOrigins: []string{"https://field.example"}}
	api := apiv1.NewConfiguredRouter(pool, client, sessions, policy)
	call := func(method, path, token, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if method == "OPTIONS" {
			r.Header.Set("Access-Control-Request-Method", "GET")
			r.Header.Set("Access-Control-Request-Headers", "Authorization, Content-Type")
		}
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, origin := range []string{"http://field.example", "https://field.example.evil", "https://field.example:444", "null"} {
		if w := call("OPTIONS", "/sessions", "", origin); w.Code != 403 || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("inexact CORS origin accepted: %s", origin)
		}
	}
	if w := call("OPTIONS", "/sessions", "", "https://field.example"); w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://field.example" || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("exact bearer preflight contract failed")
	}
	if w := call("GET", "/me", "", "https://field.example"); w.Code != 401 {
		t.Fatal("CORS granted authority")
	}
	for i, token := range []string{first, second, first, second} {
		w := call("GET", "/me", token, "https://field.example")
		want := 200
		if i == 3 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("shared user budget %d=%d", i, w.Code)
		}
		if want == 429 && (!strings.Contains(w.Body.String(), `"retry_after_seconds":60`) || w.Header().Get("Retry-After") != "60") {
			t.Fatal("untyped rate limit")
		}
	}
	api = apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 1})
	if w := call("GET", "/sync", first, ""); w.Code != 404 {
		t.Fatalf("reserved sync boundary=%d", w.Code)
	}
	if w := call("GET", "/sync", first, ""); w.Code != 429 {
		t.Fatal("client sync budget not enforced")
	}
	if w := call("GET", "/sync", second, ""); w.Code != 404 {
		t.Fatal("client budgets not independent")
	}
	if w := call("GET", "/me", first, ""); w.Code != 200 {
		t.Fatal("sync budget blocked ordinary request")
	}
	if w := call("OPTIONS", "/sessions", "", "https://field.example"); w.Code != 403 {
		t.Fatal("CORS not off by default")
	}
}

func TestHostedAuthenticationNormalizedLoginAndIPBudgetsHTTP(t *testing.T) {
	pool, client, _, sessions, _, _ := permissionHTTPFixture(t)
	web := New(pool, client, sessions, &config.Config{UploadDir: t.TempDir(), API: config.APIConfig{AuthPerLogin: 2, AuthPerIP: 3}})
	call := func(email, ip string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/login", strings.NewReader(url.Values{"email": {email}, "password": {"wrong"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = ip + ":1234"
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		return w
	}
	for i, email := range []string{" User@Test ", "user@test", "USER@TEST"} {
		w := call(email, fmt.Sprintf("192.0.2.%d", i+1))
		want := 303
		if i == 2 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("normalized login %d=%d", i, w.Code)
		}
	}
	for i := range 4 {
		w := call(fmt.Sprintf("unknown-%d@test", i), "198.51.100.1")
		want := 303
		if i == 3 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("IP budget %d=%d", i, w.Code)
		}
	}
}

func TestPairingDefaultRequestBudgetsHTTP(t *testing.T) {
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	token, err := sessions.CreateMobile(context.Background(), admin, "Default budgets")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for i := 0; i < 301; i++ {
		r := httptest.NewRequest("GET", "/me", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		want := 200
		if i == 300 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("default user attempt %d=%d", i+1, w.Code)
		}
	}
	api = apiv1.NewRouter(pool, client, sessions)
	for i := 0; i < 13; i++ {
		r := httptest.NewRequest("GET", "/sync", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		want := 404
		if i == 12 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("default sync attempt %d=%d", i+1, w.Code)
		}
	}
	for i := 0; i < 6; i++ {
		r := httptest.NewRequest("POST", "/login", strings.NewReader("email=unknown-default%40test&password=wrong"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i+1)
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		want := 303
		if i == 5 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("default login attempt %d=%d", i+1, w.Code)
		}
	}
}

func TestPairingAndHostedAuthenticationShareIPBudgetHTTP(t *testing.T) {
	pool, client, _, sessions, _, _ := permissionHTTPFixture(t)
	limits := requestlimits.New()
	policy := config.APIConfig{AuthPerIP: 2}
	web := NewWithRequestLimits(pool, client, sessions, &config.Config{UploadDir: t.TempDir(), API: policy}, limits)
	api := apiv1.NewRouterWithLimits(pool, client, sessions, policy, limits)
	for range 2 {
		r := httptest.NewRequest("POST", "/login", strings.NewReader("email=unknown%40test&password=wrong"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		if w.Code != 303 {
			t.Fatalf("login=%d", w.Code)
		}
	}
	if w := pairingCall(api, "/pairings", `{}`); w.Code != 429 {
		t.Fatalf("API bypassed hosted IP budget=%d", w.Code)
	}
}

func TestPairingMountedSyncLimitHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	token, err := sessions.CreateMobile(context.Background(), admin, "Mounted sync")
	if err != nil {
		t.Fatal(err)
	}
	mounted := chi.NewRouter()
	mounted.Mount("/api/v1", apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 1}))
	for i := range 2 {
		r := httptest.NewRequest("GET", "/api/v1/sync", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		mounted.ServeHTTP(w, r)
		want := 404
		if i == 1 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("mounted sync attempt %d=%d", i+1, w.Code)
		}
	}
	w := httptest.NewRecorder()
	mounted.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/?unknown=true", nil))
	if w.Code != 400 {
		t.Fatalf("mounted discovery filter=%d", w.Code)
	}
}

func TestHostedAuthenticationQueryBudgetHTTP(t *testing.T) {
	pool, client, _, sessions, _, _ := permissionHTTPFixture(t)
	web := New(pool, client, sessions, &config.Config{UploadDir: t.TempDir(), API: config.APIConfig{AuthPerLogin: 2}})
	for i := range 3 {
		// The hosted handler accepts FormValue, including query-backed form values.
		// Its effective normalized identity must have one budget across IPs/forms.
		r := httptest.NewRequest("POST", "/login?email=QUERY-LOGIN%40TEST", strings.NewReader("password=wrong"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i+1)
		w := httptest.NewRecorder()
		web.ServeHTTP(w, r)
		want := 303
		if i == 2 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("query-backed login attempt %d=%d", i+1, w.Code)
		}
	}
}
