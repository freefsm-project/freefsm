package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/go-chi/chi/v5"
)

func recordInvoicePaymentWithCredit(w http.ResponseWriter, r *http.Request) {
	a, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	for key, values := range r.Form {
		switch key {
		case "idempotency_key", "amount", "method", "date", "reference", "notes", "other_description", "csrf_token":
			if len(values) == 1 {
				continue
			}
		}
		http.Error(w, "invalid Payment field", http.StatusBadRequest)
		return
	}
	amount, err := parseCents(r.FormValue("amount"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	_, err = settlement.New(a.Service.Pool).RecordInvoicePaymentWithCredit(r.Context(), a.Actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: r.FormValue("idempotency_key")}, InvoiceID: id, AmountCents: amount, Method: settlement.PaymentMethod(r.FormValue("method")), ReceivedDate: settlement.Date(r.FormValue("date")), Reference: r.FormValue("reference"), Notes: r.FormValue("notes"), OtherDescription: r.FormValue("other_description")})
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Payment+receipt+confirmed", id), http.StatusSeeOther)
}
