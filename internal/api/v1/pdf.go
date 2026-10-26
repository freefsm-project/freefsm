package v1

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
)

func (h *handler) pdfSource(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			writeError(w, 400, "invalid_request", "PDF source accepts no query parameters")
			return
		}
		bound, ok := authorization.FromContext(r.Context())
		if !ok {
			unauthorized(w)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 404, "not_found", "Document not found")
			return
		}
		source, job, err := bound.Service.ReviewedPDFSource(r.Context(), bound.Actor, kind, id)
		if err != nil {
			commercialError(w, err)
			return
		}
		// Return the values the client is reviewing together with their token.
		// Ledger provenance and host filesystem paths are not field projections.
		var captured map[string]json.RawMessage
		if err := json.Unmarshal(source.Snapshot, &captured); err != nil {
			commercialError(w, err)
			return
		}
		preview := map[string]any{}
		for _, name := range []string{"document", "customer", "job", "asset", "status", "custom_field_values"} {
			preview[name] = captured[name]
		}
		var presentation map[string]json.RawMessage
		if err := json.Unmarshal(captured["presentation"], &presentation); err != nil {
			commercialError(w, err)
			return
		}
		delete(presentation, "invoice_logo_path")
		preview["presentation"] = presentation
		preview["logo_sha256"] = fmt.Sprintf("%x", sha256.Sum256(source.Logo))
		preview["logo_unavailable"] = source.LogoUnavailable
		preview["allocated_cents"] = strconv.FormatInt(source.AllocatedCents, 10)
		preview["applied_cents"] = strconv.FormatInt(source.AppliedCents, 10)
		writeJSON(w, 200, map[string]any{"document_id": id, "job_id": job, "source_token": source.ReviewToken, "source": preview})
	}
}

func (h *handler) pdfGenerate(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			writeError(w, 400, "invalid_request", "PDF generation accepts no query parameters")
			return
		}
		bound, ok := authorization.FromContext(r.Context())
		if !ok {
			unauthorized(w)
			return
		}
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, 404, "not_found", "Document not found")
			return
		}
		var in authorization.PDFIntent
		if !decodeExactJSON(w, r, &in, "operation_id", "job_id", "source_token", "depends_on", "occurred_at") {
			return
		}
		receipt, err := bound.Service.GenerateReviewedPDF(r.Context(), bound.Actor, kind, id, in, services.RenderPDFSource)
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
			writeError(w, status, receipt.Outcome, "Review the original source and dependent intent with new operation identities; no PDF was substituted")
			return
		}
		writeJSON(w, 200, receipt)
	}
}

func (h *handler) pdfReceipt(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "PDF receipts accept no query parameters")
		return
	}
	bound, ok := authorization.FromContext(r.Context())
	if !ok {
		unauthorized(w)
		return
	}
	receipt, err := bound.Service.PDFOperationReceipt(r.Context(), bound.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		commercialError(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}
