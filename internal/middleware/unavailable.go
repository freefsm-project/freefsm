package middleware

import (
	"fmt"
	"html"
	"net/http"
	"strings"
)

// ServiceUnavailable does not imply expired credentials, lost authority, an
// empty result, or a resolved mutation. Native GETs retain their URL filters in
// an explicit Retry link; enhanced clients receive an actionable plain error.
func ServiceUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "5")
	message := "The service is temporarily unavailable. Retry to check the current state. Keep the same action identity for any pending save."
	if r.Method != http.MethodGet || r.Header.Get("HX-Request") == "true" || strings.Contains(r.Header.Get("Accept"), "application/json") {
		http.Error(w, message, http.StatusServiceUnavailable)
		return
	}
	path := r.URL.RequestURI()
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		path = "/"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = fmt.Fprintf(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Service unavailable - FreeFSM</title></head><body><main><h1>Service temporarily unavailable</h1><p role="alert">%s</p><p><a href="%s">Retry</a></p></main></body></html>`, html.EscapeString(message), html.EscapeString(path))
}

// SafeReadFailures protects initial native reads as well as enhanced reads from
// legacy handler 500 bodies containing raw database errors. Successful responses
// stream normally; explicit denials and installed actionable 503s are preserved.
func SafeReadFailures(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&safeReadResponse{ResponseWriter: w, request: r}, r)
	})
}

type safeReadResponse struct {
	http.ResponseWriter
	request *http.Request
	written bool
	failed  bool
}

func (w *safeReadResponse) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *safeReadResponse) WriteHeader(code int) {
	if w.written {
		return
	}
	w.written = true
	if code >= 500 && code != http.StatusServiceUnavailable {
		w.failed = true
		w.Header().Del("Content-Length")
		w.Header().Del("Content-Disposition")
		ServiceUnavailable(w.ResponseWriter, w.request)
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *safeReadResponse) Write(body []byte) (int, error) {
	if !w.written {
		w.written = true // let the underlying writer infer the content type
	}
	if w.failed {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}
