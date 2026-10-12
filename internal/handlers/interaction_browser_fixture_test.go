package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/justinas/nosurf"
)

func newInteractionBrowserServer(t *testing.T, router http.Handler) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	web := middleware.BrowserCSRF(middleware.CSRFToken(middleware.CurrentPath(middleware.Flash(router))))
	web.SetFailureHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "CSRF validation failed: "+nosurf.Reason(r).Error(), 400)
	}))
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.Header.Get("X-Test-Lose-Response") == "true" {
			buffer := httptest.NewRecorder()
			web.ServeHTTP(buffer, r)
			if !(buffer.Code == 200 && buffer.Header().Get("File-ID") != "") && !(buffer.Code == 409 && strings.Contains(buffer.Body.String(), `"disposition":"saved"`)) {
				t.Errorf("publication must commit before transport loss: %d %s", buffer.Code, buffer.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		web.ServeHTTP(w, r)
	}))
	return httptest.NewServer(mux)
}
