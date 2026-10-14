package v1

import (
	"encoding/json"
	"errors"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"net/http"
	"strconv"
)

func (h *handler) jobFields(w http.ResponseWriter, r *http.Request) {
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 403, "forbidden", "Permission denied")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, 404, "not_found", "Job not found")
		return
	}
	var state authorization.FieldState
	if r.Method == http.MethodGet && r.URL.Query().Get("operation_id") != "" {
		receipt, receiptErr := request.Service.FieldOperationReceipt(r.Context(), request.Actor, id, r.URL.Query().Get("operation_id"))
		if receiptErr == nil {
			writeJSON(w, 200, receipt)
			return
		}
		if errors.Is(receiptErr, authorization.ErrNotFound) {
			writeError(w, 404, "not_found", "Operation not found")
		} else {
			writeError(w, 403, "forbidden", "Permission denied")
		}
		return
	}
	if r.Method == http.MethodGet {
		state, err = request.Service.ReadFieldState(r.Context(), request.Actor, id)
	} else {
		var input authorization.ReviewedFieldPatch
		if !decodeJSON(w, r, &input) {
			return
		}
		state, err = request.Service.PatchFieldsReviewed(r.Context(), request.Actor, id, input)
	}
	if err != nil {
		var fieldErr *customfields.FieldError
		var conflict *authorization.FieldConflict
		switch {
		case errors.As(err, &conflict):
			requestID := w.Header().Get("X-Request-ID")
			if requestID == "" {
				requestID = uuid.NewString()
				w.Header().Set("X-Request-ID", requestID)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": conflict.Code, "message": "Review current field values and submit a new operation", "request_id": requestID, "details": map[string]any{}, "recovery_disposition": "review", "recovery": "new_reviewed_operation"}, "comparison": state})
		case errors.Is(err, authorization.ErrNotFound):
			writeError(w, 404, "not_found", "Job not found")
		case errors.Is(err, authorization.ErrForbidden):
			writeError(w, 403, "forbidden", "Permission denied")
		case errors.As(err, &fieldErr):
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(422)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "invalid_custom_field", "message": fieldErr.Error(), "definition_id": fieldErr.ID}})
		default:
			writeError(w, 500, "internal_error", "Unable to save fields")
		}
		return
	}
	writeJSON(w, 200, state)
}
