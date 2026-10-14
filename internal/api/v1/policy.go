package v1

import (
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/requestlimits"
	"github.com/google/uuid"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ResponsePolicy applies the API's database-independent response and origin
// contract, including failures emitted before instance admission.
func ResponsePolicy(origins []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", uuid.NewString())
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			allowed := false
			for _, v := range origins {
				if origin == v {
					allowed = true
				}
			}
			// Same-origin browser requests do not require a CORS grant.
			scheme := "http"
			if middleware.IsHTTPS(r) {
				scheme = "https"
			}
			if !allowed && origin != scheme+"://"+r.Host {
				writeError(w, 403, "origin_denied", "origin is not allowed")
				return
			}
			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Expose-Headers", "Retry-After, X-Request-ID, X-FreeFSM-Session-Epoch, ETag, Repr-Digest, Content-Range, File-Revision")
				if r.Method == "OPTIONS" {
					method := r.Header.Get("Access-Control-Request-Method")
					switch method {
					case "GET", "POST", "PUT", "PATCH", "DELETE":
					default:
						writeError(w, 403, "origin_denied", "preflight method is not allowed")
						return
					}
					for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
						switch strings.ToLower(strings.TrimSpace(header)) {
						case "", "authorization", "content-type", "x-freefsm-session-epoch", "range", "if-range", "if-none-match":
						default:
							writeError(w, 403, "origin_denied", "preflight header is not allowed")
							return
						}
					}
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
					w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-FreeFSM-Session-Epoch, Range, If-Range, If-None-Match")
					w.WriteHeader(204)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) transportPolicy(next http.Handler) http.Handler {
	return ResponsePolicy(h.deps.Config.CORSOrigins, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			path := protocolPath(r)
			if strings.HasPrefix(path, "/file-transfers") || strings.HasPrefix(path, "/files/") {
				q, err := url.ParseQuery(r.URL.RawQuery)
				if err != nil || !strings.HasSuffix(path, "/chunks") || len(q) != 1 || len(q["offset"]) != 1 {
					writeError(w, 400, "invalid_request", "Only one exact chunk offset is supported; unknown filters are rejected")
					return
				}
			}
			switch protocolPath(r) {
			case "/", "/me", "/capabilities", "/sessions", "/session", "/pairings", "/pairings/redeem", "/pairings/cancel":
				writeError(w, 400, "invalid_request", "query parameters are not supported for this endpoint")
				return
			}
			if strings.HasPrefix(protocolPath(r), "/sessions/") {
				writeError(w, 400, "invalid_request", "query parameters are not supported for this endpoint")
				return
			}
		}
		next.ServeHTTP(w, r)
	}))
}
func tooMany(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	writeError(w, 429, "rate_limit_exceeded", "request budget exceeded; retry after the indicated delay")
}
func (h *handler) authenticationBudget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.limits.Allow("authip:"+requestlimits.IP(r), h.deps.Config.AuthPerIP) {
			tooMany(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (h *handler) authorizedBudget(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.limits.Allow("user:"+strconv.FormatInt(actorFromContext(r.Context()).ID, 10), h.deps.Config.RequestsPerUser) {
			tooMany(w)
			return
		}
		if protocolPath(r) == "/sync" || strings.HasPrefix(protocolPath(r), "/sync/") {
			if !h.limits.Allow("sync:"+requestlimits.ClientKey(tokenFromContext(r.Context())), h.deps.Config.SyncPerClient) {
				tooMany(w)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// chi.Mount retains URL.Path, while StripPrefix changes it. Both supported
// mounting forms must enforce the same protocol budgets and query allowlist.
func protocolPath(r *http.Request) string {
	if r.URL.Path == "/api/v1" {
		return "/"
	}
	if strings.HasPrefix(r.URL.Path, "/api/v1/") {
		return strings.TrimPrefix(r.URL.Path, "/api/v1")
	}
	return r.URL.Path
}
func (h *handler) capabilities(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	p, err := req.Service.Principal(r.Context(), req.Actor)
	if err != nil {
		sessionError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"grants": p.Grants, "limits": h.deps.Config, "window_seconds": 60})
}
