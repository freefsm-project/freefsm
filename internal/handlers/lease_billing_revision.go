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

func (h *LeaseHandler) BillingRevisions(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var values url.Values
	message := ""
	conflict, uncertain := false, false
	if r.Method == http.MethodPost {
		if err = r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		values = r.PostForm
		if values.Get("recovery") != "revise" {
			revision, e := strconv.ParseInt(values.Get("expected_revision"), 10, 64)
			in := authorization.LeaseBillingRevisionInput{ExpectedRevision: revision, EffectiveOn: values.Get("effective_on"), Reason: values.Get("reason"), OperationKey: values.Get("operation_key")}
			for _, key := range values["asset_id"] {
				asset, e1 := strconv.ParseInt(key, 10, 64)
				rent, e2 := strconv.ParseInt(values.Get("rent_cents_"+key), 10, 64)
				if e1 != nil || e2 != nil {
					e = authorization.ErrConflict
				}
				in.Changes = append(in.Changes, authorization.LeaseBillingChange{AssetID: asset, RentCents: rent, TaxRate: values.Get("tax_rate_" + key)})
			}
			if e != nil {
				message = "Enter valid Asset amounts and compare the current revision"
			} else {
				err = h.authority.ReviseLeaseBilling(r.Context(), authorizationActor(r), id, in)
				if err == nil {
					http.Redirect(w, r, fmt.Sprintf("/leases/%d/billing/revisions", id), 303)
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
					message = "Revision was not saved. Compare current terms, then revise or discard your proposal."
					conflict = true
				case errors.As(err, &failure) && failure.RolledBack:
					message = "Revision was not saved; the transaction was rolled back. Review and retry or discard your proposal."
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
	data, err := h.authority.ReviewLeaseBillingRevisions(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if values != nil && !data.CanManage {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if values == nil {
		values = url.Values{"expected_revision": {fmt.Sprint(data.Revision)}, "operation_key": {rand.Text()}}
	} else if values.Get("recovery") == "revise" {
		values.Set("expected_revision", fmt.Sprint(data.Revision))
		values.Set("operation_key", rand.Text())
		message = "Current revision loaded. Review your proposal before saving."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if message != "" {
		w.WriteHeader(409)
	}
	templates.LeaseBillingRevisionPage(data, values, message, conflict, uncertain).Render(r.Context(), w)
}
