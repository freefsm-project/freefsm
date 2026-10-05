package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	appmw "github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/go-chi/chi/v5"
)

func TestApplicationAdmissionPrecedesEveryDatabaseRoute(t *testing.T) {
	c, err := instancecontrol.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	refreshed, accessed := 0, 0
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if refreshed <= accessed {
			t.Fatal("database access before refresh")
		}
		accessed++
		w.WriteHeader(204)
	})
	status := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(202) })
	h := newApplicationHandler(app, app, instanceRoutes{control: c, refresh: func() error { refreshed++; return nil }, backup: status})
	for _, path := range []string{"/api/v1/probe", "/settings", "/delivery/open/token"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 204 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if err := c.KeepClosed(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/probe", "/settings", "/delivery/open/token"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 503 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/settings/backup/status", nil))
	if w.Code != 202 || accessed != 3 || refreshed != 3 {
		t.Fatalf("status %d, accessed %d, refreshed %d", w.Code, accessed, refreshed)
	}
}

func TestApplicationHandlerSeparatesAPICSRFAndWebRoutes(t *testing.T) {
	api := chi.NewRouter()
	api.Post("/session", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	api.Get("/probe", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("api"))
	})
	api.Get("/peer", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.RemoteAddr))
	})

	web := chi.NewRouter()
	web.Use(appmw.CSRFToken)
	web.Post("/submit", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	web.Get("/token", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(appmw.CSRFFromContext(r.Context())))
	})
	web.Get("/api/v10/probe", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("web"))
	})

	handler := newApplicationHandler(api, web)

	t.Run("API POST bypasses CSRF", func(t *testing.T) {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/api/v1/session", nil))
		if res.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", res.Code, http.StatusNoContent)
		}
	})

	t.Run("web POST remains CSRF protected", func(t *testing.T) {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/submit", nil))
		if res.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", res.Code, http.StatusBadRequest)
		}
	})

	t.Run("web CSRF token is available in context", func(t *testing.T) {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/token", nil))
		if res.Code != http.StatusOK || strings.TrimSpace(res.Body.String()) == "" {
			t.Fatalf("status = %d, token = %q", res.Code, res.Body.String())
		}
	})

	t.Run("API prefix is isolated", func(t *testing.T) {
		apiRes := httptest.NewRecorder()
		handler.ServeHTTP(apiRes, httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil))
		if apiRes.Code != http.StatusOK || apiRes.Body.String() != "api" {
			t.Fatalf("API response = %d %q", apiRes.Code, apiRes.Body.String())
		}

		webRes := httptest.NewRecorder()
		handler.ServeHTTP(webRes, httptest.NewRequest(http.MethodGet, "/api/v10/probe", nil))
		if webRes.Code != http.StatusOK || webRes.Body.String() != "web" {
			t.Fatalf("web response = %d %q", webRes.Code, webRes.Body.String())
		}
	})

	t.Run("root RealIP behavior is preserved", func(t *testing.T) {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/peer", nil)
		req.RemoteAddr = "192.0.2.10:12345"
		req.Header.Set("X-Forwarded-For", "198.51.100.7")
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK || res.Body.String() != "198.51.100.7" {
			t.Fatalf("response = %d %q", res.Code, res.Body.String())
		}
	})
}

func TestNewHTTPServerSetsProductionTimeouts(t *testing.T) {
	handler := http.NewServeMux()
	srv := newHTTPServer(":3000", handler)

	if srv.Addr != ":3000" {
		t.Fatalf("Addr = %q, want %q", srv.Addr, ":3000")
	}
	if srv.Handler != handler {
		t.Fatal("Handler was not set")
	}
	if srv.ReadHeaderTimeout != 5*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s, want 5s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 15*time.Second {
		t.Fatalf("ReadTimeout = %s, want 15s", srv.ReadTimeout)
	}
	if srv.WriteTimeout != 60*time.Second {
		t.Fatalf("WriteTimeout = %s, want 60s", srv.WriteTimeout)
	}
	if srv.IdleTimeout != 60*time.Second {
		t.Fatalf("IdleTimeout = %s, want 60s", srv.IdleTimeout)
	}
}

func TestApplicationHandlerPreservesWebPanicRecovery(t *testing.T) {
	web := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("web panic") })
	handler := newApplicationHandler(http.NotFoundHandler(), web)
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if got := res.Header().Get("Content-Type"); got == "application/json; charset=utf-8" {
		t.Fatalf("web panic unexpectedly used API content type %q", got)
	}
}
