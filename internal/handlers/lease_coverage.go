package handlers

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func (h *LeaseHandler) Coverage(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if r.FormValue("recovery") == "revise" {
			h.coverageRecovery(w, r, id, "Review your revised proposal before saving", false, true)
			return
		}
		in := authorization.LeaseCoverageInput{Action: r.FormValue("action"), Reason: r.FormValue("reason"), OperationKey: r.FormValue("operation_key"), TaxRate: r.FormValue("tax_rate")}
		in.Kind, in.EffectiveOn = r.FormValue("kind"), r.FormValue("effective_on")
		for key, target := range map[string]*int64{"event_id": &in.ID, "revision": &in.Revision, "expected_revision": &in.ExpectedRevision, "outgoing_id": &in.OutgoingID, "incoming_id": &in.IncomingID} {
			if value := r.FormValue(key); value != "" {
				number, e := strconv.ParseInt(value, 10, 64)
				if e != nil {
					h.coverageRecovery(w, r, id, "Enter a valid whole-number coverage selection", false, false)
					return
				}
				*target = number
			}
		}
		for key, target := range map[string]**int64{"rent_cents": &in.RentCents, "charge_cents": &in.ChargeCents} {
			if value := r.FormValue(key); value != "" {
				number, e := strconv.ParseInt(value, 10, 64)
				if e != nil {
					h.coverageRecovery(w, r, id, "Enter exact nonnegative whole cents for rent and charge", false, false)
					return
				}
				*target = &number
			}
		}
		if _, err = h.authority.ChangeLeaseCoverage(r.Context(), authorizationActor(r), id, in); err != nil {
			var validation *authorization.LeaseValidationError
			if errors.As(err, &validation) {
				h.coverageRecovery(w, r, id, validation.Message, false, false)
				return
			}
			if errors.Is(err, authorization.ErrConflict) {
				h.coverageRecovery(w, r, id, "Coverage could not be saved. Compare the current timeline and accepted event with your proposal, then revise or discard it.", true, false)
				return
			}
			authorizationError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/leases/%d/coverage", id), http.StatusSeeOther)
		return
	}
	data, err := h.authority.ReviewLeaseCoverage(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	choices, err := h.authority.DraftLeaseChoices(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.LeaseCoveragePage(data, choices, rand.Text()).Render(r.Context(), w)
}

// Recovery re-reads visibility rather than reflecting sensitive POST values on
// an authorization failure. Revisions advance only after an explicit review
// action, which never writes an event; the subsequent save is fenced normally.
func (h *LeaseHandler) coverageRecovery(w http.ResponseWriter, r *http.Request, id int64, message string, conflict, revise bool) {
	data, err := h.authority.ReviewLeaseCoverage(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if !data.CanManage {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	choices, err := h.authority.DraftLeaseChoices(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if selected := r.FormValue("incoming_id"); selected != "" && selected != "0" {
		allowed := false
		for _, asset := range choices.Assets {
			if fmt.Sprint(asset.ID) == selected {
				allowed = true
			}
		}
		if !allowed {
			authorizationError(w, r, authorization.ErrNotFound)
			return
		}
	}
	values := r.PostForm
	if revise {
		values.Set("expected_revision", fmt.Sprint(data.Revision))
		values.Set("operation_key", rand.Text())
		if values.Get("event_id") != "" && values.Get("event_id") != "0" {
			found := false
			for _, event := range data.Events {
				if fmt.Sprint(event.ID) == values.Get("event_id") && event.Action != "withdraw" {
					values.Set("revision", fmt.Sprint(event.Revision))
					found = true
				}
			}
			if !found {
				conflict = true
				message = "This event is no longer editable. Discard this proposal and review the accepted changes."
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusConflict)
	templates.LeaseCoverageRecoveryPage(data, choices, values, message, conflict).Render(r.Context(), w)
}
