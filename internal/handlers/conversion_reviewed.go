package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func submitReviewedConversion(w http.ResponseWriter, r *http.Request, kind string, id int64) {
	bound, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	in := conversion.ReviewedIntent{CommercialIntent: authorization.CommercialIntent{OperationID: r.FormValue("idempotency_key"), Revision: r.FormValue("revision")}, CycleID: r.FormValue("cycle_id")}
	w.Header().Set("X-FreeFSM-Receipt", "/commercial-operations/"+in.OperationID)
	if fields := parseCustomFieldPatch(r); fields != nil {
		raw := json.RawMessage(*fields)
		in.InvoiceFields = &raw
	}
	receipt, err := conversion.New(bound.Service.Pool).ExecuteReviewed(r.Context(), kind, id, in)
	if err != nil {
		writeConversionError(w, r, err)
		return
	}
	if receipt.Outcome != "accepted" {
		status := http.StatusConflict
		switch receipt.Outcome {
		case "review_required":
			status = http.StatusPreconditionRequired
		case "forbidden":
			status = 403
		case "not_found":
			status = 404
		case "invalid_document":
			status = 422
		}
		http.Error(w, receipt.Outcome+": review the current document and cycle; submit a new operation identity", status)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/%ss/%d", receipt.DocumentType, receipt.ID), http.StatusSeeOther)
}

func commercialReceiptWeb(w http.ResponseWriter, r *http.Request) {
	bound, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	receipt, err := bound.Service.CommercialReceipt(r.Context(), bound.Actor, chi.URLParam(r, "operation"))
	if err != nil {
		writeConversionError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(receipt)
}

func (h *InvoiceHandler) ReversionPreview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	bound, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	tx, err := bound.Service.Pool.Begin(r.Context())
	if err != nil {
		internalServerError(w, r, "revert preview", err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = bound.Service.AuthorizeReversion(r.Context(), tx, bound.Actor, id); err != nil {
		authorizationError(w, r, err)
		return
	}
	var cycle string
	if err = tx.QueryRow(r.Context(), `SELECT id::text FROM estimate_invoice_conversion_cycles WHERE invoice_id=$1 AND reverted_at IS NULL`, id).Scan(&cycle); err != nil {
		http.NotFound(w, r)
		return
	}
	revision, err := saveaction.Revision(r.Context(), tx, bound.Actor.UserID, fmt.Sprintf("/invoices/%d", id))
	if err != nil {
		internalServerError(w, r, "revert preview", err)
		return
	}
	templates.ReversionPreview(id, uuid.NewString(), revision, cycle).Render(r.Context(), w)
}
