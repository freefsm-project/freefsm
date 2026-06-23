package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRateLimiterLimitsByIPAndPath(t *testing.T) {
	limiter := NewRateLimiter(2, time.Minute)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "192.0.2.10:12345"

	if !limiter.Allow(req) {
		t.Fatal("first request rejected")
	}
	if !limiter.Allow(req) {
		t.Fatal("second request rejected")
	}
	if limiter.Allow(req) {
		t.Fatal("third request allowed")
	}

	otherPath := httptest.NewRequest(http.MethodPost, "/setup", nil)
	otherPath.RemoteAddr = req.RemoteAddr
	if !limiter.Allow(otherPath) {
		t.Fatal("request to different path rejected")
	}
}

func TestRateLimiterResetsAfterWindow(t *testing.T) {
	limiter := NewRateLimiter(1, 10*time.Millisecond)
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "192.0.2.10:12345"

	if !limiter.Allow(req) {
		t.Fatal("first request rejected")
	}
	if limiter.Allow(req) {
		t.Fatal("second request allowed")
	}

	time.Sleep(20 * time.Millisecond)
	if !limiter.Allow(req) {
		t.Fatal("request after window rejected")
	}
}

func TestRateLimiterHandlerReturnsTooManyRequests(t *testing.T) {
	limiter := NewRateLimiter(1, time.Minute)
	handler := limiter.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/login", nil)
	req.RemoteAddr = "192.0.2.10:12345"

	handler.ServeHTTP(httptest.NewRecorder(), req)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("Retry-After header is empty")
	}
}
