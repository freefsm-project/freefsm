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
		id, err = request.Service.ResolveSyncedJob(r.Context(), chi.URLParam(r, "id"))
	}
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
		var wire struct {
			authorization.ReviewedFieldPatch
			Revisions json.RawMessage `json:"revisions"`
		}
		if !decodeExactJSON(w, r, &wire, "operation_id", "revisions", "values", "occurred_at") {
			return
		}
		input := wire.ReviewedFieldPatch
		// Preserve the established invalid-operation response without admission.
		if id, parseErr := uuid.Parse(input.OperationID); parseErr == nil && id != uuid.Nil {
			var valid bool
			input.Revisions, valid = fieldPatchWire(w, r, input.Values, wire.Revisions)
			if !valid {
				return
			}
		}
		state, err = request.Service.PatchFieldsReviewed(r.Context(), request.Actor, id, input)
	}
	if err != nil {
		var fieldErr *customfields.FieldError
		var conflict *authorization.FieldConflict
		switch {
		case errors.As(err, &conflict):
			details := map[string]any{}
			if state.Receipt != nil && state.Receipt.Reason != "" {
				details["reason"] = state.Receipt.Reason
			}
			requestID := w.Header().Get("X-Request-ID")
			if requestID == "" {
				requestID = uuid.NewString()
				w.Header().Set("X-Request-ID", requestID)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"status": http.StatusConflict, "code": conflict.Code, "message": "Review current field values and submit a new operation", "request_id": requestID, "details": details, "recovery_disposition": "review", "recovery": "new_reviewed_operation"}, "comparison": state})
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

func (h *handler) jobFieldReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	receipt, err := req.Service.JobFieldExecutionReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if errors.Is(err, authorization.ErrConflict) {
		writeError(w, 409, "operation_identity_conflict", "Original field operation binding requires review")
		return
	}
	if errors.Is(err, authorization.ErrNotFound) {
		writeError(w, 404, "not_found", "Operation not found")
		return
	}
	if errors.Is(err, authorization.ErrForbidden) {
		writeError(w, 403, "forbidden", "Permission denied")
		return
	}
	if err != nil {
		writeError(w, 503, "unavailable", "Receipt unavailable")
		return
	}
	writeJSON(w, 200, receipt)
}
