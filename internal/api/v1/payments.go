package v1

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/go-chi/chi/v5"
)

func (h *handler) boundedPayment(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 403, "forbidden", "Permission denied")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 404, "not_found", "Invoice not found")
		return
	}
	var in settlement.BoundedIntent
	if !decodeExactJSON(w, r, &in, "operation_id", "job_id", "amount", "method", "date", "reference", "note", "occurred_at", "other_description") {
		return
	}
	receipt, err := settlement.New(req.Service.Pool).AcceptBoundedPayment(r.Context(), req.Actor, id, in)
	if err != nil {
		paymentError(w, err)
		return
	}
	status := 200
	switch receipt.Outcome {
	case "not_found":
		status = 404
	case "forbidden":
		status = 403
	case "invalid_payment":
		status = 422
	case "invoice_ineligible":
		status = 409
	}
	if status != 200 {
		writeError(w, status, receipt.Outcome, "Review the original receipt and current Invoice before proposing a new fixed Payment")
		return
	}
	writeJSON(w, 200, receipt)
}

func (h *handler) paymentReceipt(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 403, "forbidden", "Permission denied")
		return
	}
	receipt, err := settlement.New(req.Service.Pool).PaymentReceipt(r.Context(), req.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		paymentError(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}

func (h *handler) paymentMethods(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		writeError(w, 403, "forbidden", "Permission denied")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 404, "not_found", "Invoice not found")
		return
	}
	// The public field API never offers a Customer-only fallback.
	if _, err := req.Service.ReadCommercial(r.Context(), req.Actor, "invoice", id); err != nil {
		paymentError(w, err)
		return
	}
	choices, err := settlement.New(req.Service.Pool).PaymentMethods(r.Context(), req.Actor, id)
	if err != nil {
		paymentError(w, err)
		return
	}
	writeJSON(w, 200, choices)
}

func paymentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, authorization.ErrNotFound):
		writeError(w, 404, "not_found", "Payment operation not found")
	case errors.Is(err, authorization.ErrForbidden):
		writeError(w, 403, "forbidden", "Permission denied")
	case errors.Is(err, authorization.ErrConflict):
		writeError(w, 409, "operation_intent_changed", "Operation identity already belongs to another intent")
	case errors.Is(err, settlement.ErrInvalidAmount):
		writeError(w, 422, "invalid_payment", "A valid fixed Payment and operation identity are required")
	default:
		writeError(w, 503, "temporarily_unavailable", "Payment status unknown; reconcile the original operation before another intent")
	}
}
