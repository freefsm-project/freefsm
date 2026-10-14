package v1

import (
	"context"
	"errors"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"strconv"
)

type sessionEpochKey struct{}

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
	operation, ok := securityOperationID(w, r)
	if !ok {
		return
	}
	kind := "revoke_all"
	if id != 0 {
		kind = "revoke"
	}
	if _, err := req.Service.SubmitSessionRevocation(r.Context(), req.Actor, operation, kind, id); err != nil {
		sessionError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func securityOperationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Method == http.MethodDelete && r.ContentLength != 0 {
		writeError(w, 400, "invalid_request", "session revocation does not accept a request body")
		return "", false
	}
	values := r.Header.Values("Operation-ID")
	if len(values) == 1 {
		id, err := uuid.Parse(values[0])
		if err == nil && id != uuid.Nil {
			return id.String(), true
		}
	}
	writeError(w, 400, "invalid_operation_id", "one client-generated Operation-ID UUID is required")
	return "", false
}

func (h *handler) sessionReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	result, err := req.Service.SessionRevocationReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		sessionError(w, err)
		return
	}
	writeJSON(w, 200, result)
}

func sessionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authorization.ErrInvalidSessionOperation):
		writeError(w, 400, "invalid_operation_id", "invalid session operation identity")
	case errors.Is(err, authorization.ErrConflict):
		writeError(w, 409, "operation_intent_changed", "operation identity is already bound")
	case errors.Is(err, authorization.ErrNotFound):
		writeError(w, 404, "not_found", "session not found")
	case errors.Is(err, authorization.ErrForbidden):
		writeError(w, 403, "forbidden", "session access denied")
	default:
		writeError(w, 503, "unavailable", "session management unavailable")
	}
}
