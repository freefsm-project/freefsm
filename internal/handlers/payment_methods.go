package handlers

import (
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"net/http"
)

func paymentMethodsSettings(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	svc := settlement.New(req.Service.Pool)
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		if err := svc.RenamePaymentMethod(r.Context(), req.Actor, settlement.PaymentMethod(chi.URLParam(r, "method")), r.FormValue("label")); err != nil {
			writeSettlementError(w, r, err)
			return
		}
		http.Redirect(w, r, "/settings/payment-methods", 303)
		return
	}
	choices, err := svc.ConfiguredPaymentMethods(r.Context(), req.Actor)
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	p, err := req.Service.Principal(r.Context(), req.Actor)
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	_ = templates.PaymentMethodSettings(choices, p.Allows("configuration.company.manage", authorization.Configuration)).Render(r.Context(), w)
}
