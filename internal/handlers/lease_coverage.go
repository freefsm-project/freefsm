package handlers

import (
	"crypto/rand"
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
		in := authorization.LeaseCoverageInput{Action: r.FormValue("action"), Reason: r.FormValue("reason"), OperationKey: r.FormValue("operation_key"), TaxRate: r.FormValue("tax_rate")}
		in.Kind, in.EffectiveOn = r.FormValue("kind"), r.FormValue("effective_on")
		for key, target := range map[string]*int64{"event_id": &in.ID, "revision": &in.Revision, "expected_revision": &in.ExpectedRevision, "outgoing_id": &in.OutgoingID, "incoming_id": &in.IncomingID} {
			if value := r.FormValue(key); value != "" {
				number, e := strconv.ParseInt(value, 10, 64)
				if e != nil {
					http.Error(w, "Invalid coverage selection", 400)
					return
				}
				*target = number
			}
		}
		for key, target := range map[string]**int64{"rent_cents": &in.RentCents, "charge_cents": &in.ChargeCents} {
			if value := r.FormValue(key); value != "" {
				number, e := strconv.ParseInt(value, 10, 64)
				if e != nil {
					http.Error(w, "Enter exact nonnegative cents", 400)
					return
				}
				*target = &number
			}
		}
		if _, err = h.authority.ChangeLeaseCoverage(r.Context(), authorizationActor(r), id, in); err != nil {
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
