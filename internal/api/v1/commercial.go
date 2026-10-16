package v1

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/go-chi/chi/v5"
)

func (h *handler) commercialLifecycle(kind string, finalize bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			writeError(w, 403, "forbidden", "Permission denied")
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 404, "not_found", "Document not found")
			return
		}
		var in statusflow.CommercialIntent
		if !decodeExactJSON(w, r, &in, "operation_id", "job_id", "revision", "occurred_at", "status_id", "meaning_revision") {
			return
		}
		receipt, err := statusflow.New(req.Service.Pool).ApplyCommercial(r.Context(), req.Actor, statusflow.ObjectType(kind), id, finalize, in)
		if err != nil {
			commercialError(w, err)
			return
		}
		status := commercialOutcomeStatus(receipt.Outcome)
		if status != 200 {
			writeError(w, status, receipt.Outcome, "Review the current document and workflow; explicitly reselect with a new operation identity")
			return
		}
		writeJSON(w, status, receipt)
	}
}

func (h *handler) commercial(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		req, ok := authorization.FromContext(r.Context())
		if !ok {
			writeError(w, 403, "forbidden", "Permission denied")
			return
		}
		var id int64
		if text := chi.URLParam(r, "id"); text != "" {
			var err error
			id, err = strconv.ParseInt(text, 10, 64)
			if err != nil || id <= 0 {
				writeError(w, 404, "not_found", "Document not found")
				return
			}
		}
		if r.Method == http.MethodGet {
			state, err := req.Service.ReadCommercial(r.Context(), req.Actor, kind, id)
			if err != nil {
				commercialError(w, err)
				return
			}
			if raw, ok := state["line_items"].(json.RawMessage); ok {
				financial := ""
				version := 0
				if value, ok := state["financial"].(json.RawMessage); ok {
					financial = string(value)
					version = 1
				}
				tax, _ := state["tax_rate"].(string)
				totals, err := services.FinancialTotals(financial, string(raw), tax)
				if err != nil {
					commercialError(w, err)
					return
				}
				state["arithmetic_version"] = version
				state["totals"] = map[string]string{"subtotal": totals.Subtotal.Format(), "tax": totals.Tax.Format(), "total": totals.Total.Format()}
				lines, err := services.PublicCommercialLines(string(raw))
				if err != nil {
					commercialError(w, err)
					return
				}
				state["line_items"] = lines
			}
			writeJSON(w, 200, state)
			return
		}
		var in services.CommercialInput
		if !decodeExactJSON(w, r, &in, "operation_id", "job_id", "client_id", "revision", "occurred_at", "title", "notes", "terms", "tax_rate", "invoice_date", "due_date", "valid_through", "line_items", "custom_fields", "adjustments") {
			return
		}
		receipt, err := services.SaveCommercialAPI(r.Context(), kind, id, in)
		if err != nil {
			commercialError(w, err)
			return
		}
		status := commercialOutcomeStatus(receipt.Outcome)
		if status != 200 {
			writeError(w, status, receipt.Outcome, "Review the current authorized document and submit a new operation identity; this intent will not be retried automatically")
			return
		}
		writeJSON(w, status, receipt)
	}
}

func (h *handler) commercialReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 403, "forbidden", "Permission denied")
		return
	}
	receipt, err := req.Service.CommercialReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		commercialError(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}

func commercialOutcomeStatus(outcome string) int {
	switch outcome {
	case "accepted":
		return 200
	case "not_found":
		return 404
	case "forbidden":
		return 403
	case "invalid_document":
		return 422
	default:
		return 409
	}
}

func commercialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		writeError(w, 404, "not_found", "Document not found")
	case errors.Is(err, authorization.ErrForbidden):
		writeError(w, 403, "forbidden", "Permission denied")
	case errors.Is(err, authorization.ErrConflict):
		writeError(w, 409, "operation_intent_changed", "Review and submit a new operation identity")
	case errors.Is(err, authorization.ErrCommercialInput):
		writeError(w, 422, "invalid_document", "A valid operation identity, Job and document inputs are required")
	default:
		writeError(w, 503, "temporarily_unavailable", "Document save status is unknown; query the original operation before retrying")
	}
}
