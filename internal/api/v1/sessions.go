package v1

import (
	"context"
	"errors"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

type sessionEpochKey struct{}

func jobSyncDiscovery(p authorization.Principal) map[string]any {
	scopes := []string{}
	if p.Allows("job.view", authorization.Assigned) {
		scopes = append(scopes, "assigned")
	}
	if p.Allows("job.view", authorization.All) {
		scopes = append(scopes, "all")
	}
	return map[string]any{"endpoint": "/api/v1/sync/jobs", "available": len(scopes) > 0, "scopes": scopes, "default_page_size": 50, "max_page_size": 200, "continuity_days": 30, "filters": []string{"scope"}}
}

func (h *handler) sessionEpoch(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasBearer := bearerToken(r.Header.Get("Authorization"))
		if h.deps.Authorization != nil && hasBearer {
			var epoch string
			if err := h.deps.Authorization.Pool.QueryRow(r.Context(), `SELECT epoch::text FROM session_instance WHERE singleton`).Scan(&epoch); err != nil {
				writeError(w, 503, "unavailable", "session recovery state unavailable")
				return
			}
			if previous := r.Header.Get("X-FreeFSM-Session-Epoch"); previous != "" && previous != epoch {
				writeError(w, 401, "instance_restored", "sign in again and synchronize current authority before reviewing pending work")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), sessionEpochKey{}, epoch))
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) sessions(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	if r.Method == http.MethodGet {
		list, err := req.Service.Sessions(r.Context(), req.Actor, req.Actor.UserID)
		if err != nil {
			sessionError(w, err)
			return
		}
		writeJSON(w, 200, list)
		return
	}
	var id int64
	if raw := chi.URLParam(r, "sessionID"); raw != "" {
		var err error
		id, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 404, "not_found", "session not found")
			return
		}
	}
	if err := req.Service.RevokeSessions(r.Context(), req.Actor, req.Actor.UserID, id); err != nil {
		sessionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func sessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		writeError(w, 404, "not_found", "session not found")
	case errors.Is(err, authorization.ErrForbidden):
		writeError(w, 403, "forbidden", "session access denied")
	default:
		writeError(w, 503, "unavailable", "session management unavailable")
	}
}
