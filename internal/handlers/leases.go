package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type LeaseHandler struct{ authority *authorization.Service }

func (h *LeaseHandler) Register(w http.ResponseWriter, r *http.Request) {
	customer, err := strconv.ParseInt(r.FormValue("customer_id"), 10, 64)
	if err != nil || customer < 1 {
		http.Error(w, "Select an active Customer", http.StatusBadRequest)
		return
	}
	lease, err := h.authority.RegisterDraftLease(r.Context(), authorizationActor(r), customer)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/leases/%d", lease.ID), http.StatusSeeOther)
}

func (h *LeaseHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	lease, err := h.authority.DraftLeaseRegistration(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	templates.LeaseRegistrationPage(lease.Number).Render(r.Context(), w)
}
