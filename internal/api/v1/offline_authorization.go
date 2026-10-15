package v1

import (
	"errors"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func offlineAPIError(w http.ResponseWriter, err error) {
	status, code := 503, "unavailable"
	switch {
	case errors.Is(err, authorization.ErrOfflineDisabled):
		status, code = 403, "offline_disabled"
	case errors.Is(err, authorization.ErrOfflineOperation):
		status, code = 400, "invalid_operation_id"
	case errors.Is(err, authorization.ErrConflict):
		status, code = 409, "operation_intent_changed"
	case errors.Is(err, authorization.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, authorization.ErrNotFound):
		status, code = 404, "not_found"
	}
	if status == 503 {
		w.Header().Set("Retry-After", "5")
	}
	writeError(w, status, code, "offline authorization request could not be completed")
}

func (h *handler) offlinePolicy(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "query parameters are not supported")
		return
	}
	hours, err := req.Service.OfflinePolicy(r.Context(), req.Actor)
	if err != nil {
		offlineAPIError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": hours > 0, "hours": hours, "maximum_hours": 72})
}
func (h *handler) offlineLease(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "query parameters are not supported")
		return
	}
	var in *struct{}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in == nil {
		writeError(w, 400, "invalid_request", "lease issuance requires an empty object")
		return
	}
	operation, ok := securityOperationID(w, r)
	if !ok {
		return
	}
	result, err := req.Service.IssueOfflineAuthorization(r.Context(), req.Actor, operation)
	if err != nil {
		offlineAPIError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (h *handler) offlineLeaseReceipt(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "query parameters are not supported")
		return
	}
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	result, err := req.Service.OfflineAuthorizationReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		offlineAPIError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
