package v1

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/go-chi/chi/v5"
)

func (h *handler) conversionPreview(w http.ResponseWriter, r *http.Request) {
	bound, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 403, "forbidden", "Permission denied")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 404, "not_found", "Document not found")
		return
	}
	var in struct {
		Fields *json.RawMessage `json:"custom_fields"`
	}
	if r.Method == "POST" && !decodeExactJSON(w, r, &in, "custom_fields") {
		return
	}
	preview, err := conversion.New(bound.Service.Pool).Preview(r.Context(), id, in.Fields)
	if err != nil {
		commercialError(w, err)
		return
	}
	writeJSON(w, 200, preview)
}

func (h *handler) conversion(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		bound, ok := authorization.FromContext(r.Context())
		if !ok {
			writeError(w, 403, "forbidden", "Permission denied")
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 404, "not_found", "Document not found")
			return
		}
		var in conversion.ReviewedIntent
		if !decodeExactJSON(w, r, &in, "operation_id", "job_id", "revision", "occurred_at", "cycle_id", "custom_fields") {
			return
		}
		receipt, err := conversion.New(bound.Service.Pool).ExecuteReviewed(r.Context(), kind, id, in)
		if err != nil {
			commercialError(w, err)
			return
		}
		status := commercialOutcomeStatus(receipt.Outcome)
		if status != 200 {
			writeError(w, status, receipt.Outcome, "Review the current document and exact cycle; never automatically unwind or resubmit")
			return
		}
		writeJSON(w, 200, receipt)
	}
}
