package v1

import (
	"encoding/json"
	"errors"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/go-chi/chi/v5"
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
	var values string
	if r.Method == http.MethodGet {
		values, err = request.Service.ReadFields(r.Context(), request.Actor, "job", id, false)
	} else {
		var input struct {
			Values json.RawMessage `json:"values"`
		}
		if !decodeJSON(w, r, &input) {
			return
		}
		values, err = request.Service.PatchFields(r.Context(), request.Actor, "job", id, string(input.Values))
	}
	if err != nil {
		var fieldErr *customfields.FieldError
		switch {
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
	writeJSON(w, 200, map[string]any{"values": json.RawMessage(values)})
}
