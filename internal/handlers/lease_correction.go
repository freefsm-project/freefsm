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

func (h *LeaseHandler) Correction(w http.ResponseWriter, r *http.Request) {
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
			premises, e2 := strconv.ParseInt(values.Get("premises_id"), 10, 64)
			if e != nil || e2 != nil {
				message = "Select valid premises and compare the current revision"
				conflict = true
			} else {
				err = h.authority.CorrectLeaseFacts(r.Context(), authorizationActor(r), id, authorization.LeaseFactInput{ExpectedRevision: revision, StartsOn: values.Get("starts_on"), EndsOn: values.Get("ends_on"), PremisesID: premises, Reason: values.Get("reason"), OperationKey: values.Get("operation_key"), Factual: values.Get("factual") == "true"})
				if err == nil {
					http.Redirect(w, r, fmt.Sprintf("/leases/%d/correction", id), http.StatusSeeOther)
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
					message = "Correction was not saved. Compare current facts, then revise or discard your proposal."
					conflict = true
				case errors.As(err, &failure) && failure.RolledBack:
					message = "Correction was not saved; the transaction was rolled back. Review and retry or discard your proposal."
				case errors.As(err, &failure):
					message = "Outcome uncertain. Compare saved facts and retry only the exact submission to reconcile its identity."
					uncertain = true
				default:
					authorizationError(w, r, err)
					return
				}
			}
		}
	}
	data, err := h.authority.ReviewLeaseFacts(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if values != nil && !data.CanManage {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	if values == nil {
		values = url.Values{"expected_revision": {fmt.Sprint(data.Base.Revision)}, "operation_key": {rand.Text()}, "starts_on": {data.Base.StartsOn}, "ends_on": {data.Base.EndsOn}}
		if data.Base.PremisesID != nil {
			values.Set("premises_id", fmt.Sprint(*data.Base.PremisesID))
		}
	} else if values.Get("recovery") == "revise" {
		values.Set("expected_revision", fmt.Sprint(data.Base.Revision))
		values.Set("operation_key", rand.Text())
		message = "Current revision loaded. Review your proposal before saving."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if message != "" {
		w.WriteHeader(http.StatusConflict)
	}
	templates.LeaseCorrectionPage(data, values, message, conflict, uncertain).Render(r.Context(), w)
}
