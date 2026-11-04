package handlers

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func (h *LeaseHandler) Adjustments(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	selected, _ := strconv.ParseInt(r.URL.Query().Get("invoice_id"), 10, 64)
	var values url.Values
	message := ""
	conflict, uncertain := false, false
	if r.Method == http.MethodPost {
		if err = r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		values = r.PostForm
		selected, _ = strconv.ParseInt(values.Get("invoice_id"), 10, 64)
		if values.Get("recovery") != "revise" {
			asset, e1 := strconv.ParseInt(values.Get("asset_id"), 10, 64)
			amount, e2 := strconv.ParseInt(values.Get("amount_cents"), 10, 64)
			if e1 != nil || e2 != nil {
				message = "Enter a valid Asset and whole USD cent amount"
			} else {
				err = h.authority.ApplyLeaseAdjustment(r.Context(), authorizationActor(r), id, authorization.LeaseAdjustmentInput{InvoiceID: selected, AssetID: asset, AmountCents: amount, Kind: values.Get("kind"), StartsOn: values.Get("starts_on"), EndsOn: values.Get("ends_on"), Reason: values.Get("reason"), OperationKey: values.Get("operation_key"), InvoiceRevision: values.Get("invoice_revision"), Taxable: values.Get("taxable") == "true"})
				if err == nil {
					http.Redirect(w, r, fmt.Sprintf("/leases/%d/billing/adjustments?invoice_id=%d", id, selected), 303)
					return
				}
				var validation *authorization.LeaseValidationError
				var failure *authorization.TransactionFailure
				switch {
				case errors.Is(err, authorization.ErrForbidden), errors.Is(err, authorization.ErrNotFound):
					authorizationError(w, r, err)
					return
				case errors.As(err, &validation):
					message = validation.Message
				case errors.Is(err, authorization.ErrConflict):
					message = "Adjustment was not saved. Compare the current Invoice, then revise or discard your proposal."
					conflict = true
				case errors.As(err, &failure) && failure.RolledBack:
					message = "Adjustment was not saved; the transaction was rolled back. Review and retry or discard your proposal."
				case errors.As(err, &failure):
					message = "Outcome uncertain. Retry only the exact submission to reconcile its identity."
					uncertain = true
				default:
					authorizationError(w, r, err)
					return
				}
			}
		}
	}
	data, err := h.authority.ReviewLeaseAdjustments(r.Context(), authorizationActor(r), id, selected)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if values != nil && !data.CanManage {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if values == nil {
		values = url.Values{"invoice_id": {fmt.Sprint(selected)}, "invoice_revision": {data.Selected.Revision}, "operation_key": {rand.Text()}}
	} else if values.Get("recovery") == "revise" {
		values.Set("invoice_revision", data.Selected.Revision)
		values.Set("operation_key", rand.Text())
		message = "Current Invoice loaded. Review your proposal before applying."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if message != "" {
		w.WriteHeader(409)
	}
	templates.LeaseAdjustmentPage(data, values, message, conflict, uncertain).Render(r.Context(), w)
}
