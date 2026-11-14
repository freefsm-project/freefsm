package v1

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/go-chi/chi/v5"
)

var deliveryFields = []string{"pdf_version_id", "confirm_stale_pdf", "to", "cc", "bcc", "subject", "text_body", "html_body", "extra_file_ids"}

func deliveryTarget(w http.ResponseWriter, r *http.Request, kind string) (delivery.DocumentRef, bool) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Delivery endpoints accept no query parameters")
		return delivery.DocumentRef{}, false
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, 404, "not_found", "Document not found")
		return delivery.DocumentRef{}, false
	}
	return delivery.DocumentRef{Type: kind, ID: id}, true
}
func (h *handler) deliveryReview(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target, ok := deliveryTarget(w, r, kind)
		if !ok {
			return
		}
		var in delivery.MessageIntent
		if !decodeExactJSON(w, r, &in, deliveryFields...) {
			return
		}
		if h.deps.Delivery == nil {
			writeError(w, 503, "unavailable", "Delivery service unavailable")
			return
		}
		review, err := h.deps.Delivery.Review(r.Context(), target, in)
		if err != nil {
			if errors.Is(err, authorization.ErrConflict) {
				writeError(w, 409, "stale_pdf", "Review and explicitly confirm this exact stale PDF version")
				return
			}
			deliveryError(w, err)
			return
		}
		writeJSON(w, 200, review)
	}
}
func (h *handler) deliveryQueue(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target, ok := deliveryTarget(w, r, kind)
		if !ok {
			return
		}
		var in delivery.ReviewedIntent
		fields := append(append([]string{}, deliveryFields...), "operation_id", "review_token", "job_id", "occurred_at", "pdf_operation_id")
		if !decodeExactJSON(w, r, &in, fields...) {
			return
		}
		if h.deps.Delivery == nil {
			writeError(w, 503, "unavailable", "Delivery service unavailable")
			return
		}
		receipt, err := h.deps.Delivery.QueueReviewed(r.Context(), target, in)
		if err != nil {
			deliveryError(w, err)
			return
		}
		if receipt.Outcome != "accepted" {
			status := 409
			if receipt.Outcome == "forbidden" {
				status = 403
			}
			if receipt.Outcome == "not_found" {
				status = 404
			}
			if receipt.Outcome == "invalid_attachments" {
				status = 422
			}
			writeError(w, status, receipt.Outcome, "Review original document, exact PDF and message with a new operation identity")
			return
		}
		writeJSON(w, 200, receipt)
	}
}
func (h *handler) deliveryReceipt(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Delivery receipts accept no query parameters")
		return
	}
	bound, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	receipt, err := bound.Service.DeliveryOperationReceipt(r.Context(), bound.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		commercialError(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}
func deliveryError(w http.ResponseWriter, err error) {
	if errors.Is(err, delivery.ErrIdempotencyConflict) {
		writeError(w, 409, "operation_intent_changed", "Review and submit a new operation identity")
		return
	}
	var invalid *authorization.FileValidationError
	if errors.As(err, &invalid) || errors.Is(err, delivery.ErrInvalid) {
		writeError(w, 422, "invalid_attachments", "Invalid Delivery attachments or message")
		return
	}
	commercialError(w, err)
}

func (h *handler) deliveryRetry(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Delivery retries accept no query parameters")
		return
	}
	var in delivery.RetryIntent
	if !decodeExactJSON(w, r, &in, "operation_id", "document_type", "document_id", "job_id", "delivery_id", "reason", "occurred_at") {
		return
	}
	if h.deps.Delivery == nil {
		writeError(w, 503, "unavailable", "Delivery service unavailable")
		return
	}
	receipt, err := h.deps.Delivery.Retry(r.Context(), in)
	if err != nil {
		commercialError(w, err)
		return
	}
	if receipt.Outcome != "accepted" {
		status := 409
		if receipt.Outcome == "forbidden" {
			status = 403
		}
		if receipt.Outcome == "not_found" {
			status = 404
		}
		writeError(w, status, receipt.Outcome, "Reconcile the original retry receipt; repair requires new reviewed intent")
		return
	}
	writeJSON(w, 200, receipt)
}
