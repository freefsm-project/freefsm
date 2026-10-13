package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/requestlimits"
	"github.com/google/uuid"
)

func authenticationLimits(policy config.APIConfig, limiter *requestlimits.Limiter) func(http.Handler) http.Handler {
	policy = policy.Defaults()
	if limiter == nil {
		limiter = requestlimits.New()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Invalid authentication form", 400)
				return
			}
			allowed := limiter.Allow("authip:"+requestlimits.IP(r), policy.AuthPerIP)
			// Match the hosted handler's effective form identity, including
			// query-backed FormValue inputs; otherwise changing transport bypasses
			// the normalized-login budget while still reaching authentication.
			if login := r.FormValue("email"); login != "" {
				allowed = limiter.Allow(requestlimits.LoginKey(login), policy.AuthPerLogin) && allowed
			}
			if !allowed {
				id := uuid.NewString()
				w.Header().Set("X-Request-ID", id)
				w.Header().Set("Retry-After", "60")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(429)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "rate_limit_exceeded", "message": "authentication budget exceeded", "request_id": id, "details": map[string]any{}, "recovery_disposition": "retry", "retry_after_seconds": 60}})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
