package v1

import (
	"errors"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

func subtaskAPIError(w http.ResponseWriter, err error) {
	status, code := 503, "retryable_failure"
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		status, code = 404, "not_found"
	case errors.Is(err, authorization.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(err, authorization.ErrConflict):
		status, code = 409, "subtask_conflict"
	case errors.Is(err, authorization.ErrSubtaskInvalid):
		status, code = 400, "invalid_subtask"
	case errors.Is(err, authorization.ErrSubtaskReview):
		status, code = 409, "review_required"
	}
	writeError(w, status, code, code)
}
func (h *handler) subtasks(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 404, "not_found", "not found")
		return
	}
	result, err := req.Service.Subtasks(r.Context(), req.Actor, id)
	if err != nil {
		subtaskAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *handler) subtaskReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 503, "unavailable", "authorization unavailable")
		return
	}
	result, err := req.Service.SubtaskReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		subtaskAPIError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
