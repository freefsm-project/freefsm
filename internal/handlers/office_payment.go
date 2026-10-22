package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

func recordInvoicePaymentWithCredit(w http.ResponseWriter, r *http.Request) {
	recordOfficePayment(w, r, false)
}

func recordCustomerPayment(w http.ResponseWriter, r *http.Request) { recordOfficePayment(w, r, true) }

func recordOfficePayment(w http.ResponseWriter, r *http.Request, customer bool) {
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
		if customer && key == "invoice_id" && len(values) == 1 {
			continue
		}
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
	req := settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: r.FormValue("idempotency_key")}, InvoiceID: id, AmountCents: amount, Method: settlement.PaymentMethod(r.FormValue("method")), ReceivedDate: settlement.Date(r.FormValue("date")), Reference: r.FormValue("reference"), Notes: r.FormValue("notes"), OtherDescription: r.FormValue("other_description")}
	if customer {
		req.InvoiceID = 0
		if raw := r.FormValue("invoice_id"); raw != "" {
			req.InvoiceID, err = strconv.ParseInt(raw, 10, 64)
			if err != nil || req.InvoiceID <= 0 {
				http.Error(w, "invalid Invoice", 400)
				return
			}
		}
		_, err = settlement.New(a.Service.Pool).RecordCustomerPayment(r.Context(), a.Actor, id, req)
	} else {
		_, err = settlement.New(a.Service.Pool).RecordInvoicePaymentWithCredit(r.Context(), a.Actor, req)
	}
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	if r.Header.Get("Accept") == "application/json" {
		receipt, err := settlement.New(a.Service.Pool).PaymentReceipt(r.Context(), a.Actor, req.Operation.Key)
		if err != nil {
			writeSettlementError(w, r, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": receipt})
		return
	}
	if customer {
		http.Redirect(w, r, fmt.Sprintf("/customers/%d/payments?flash=Payment+receipt+confirmed", id), 303)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/invoices/%d?flash=Payment+receipt+confirmed", id), http.StatusSeeOther)
}

func customerReceipts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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
	page, err := settlement.New(a.Service.Pool).CustomerReceipts(r.Context(), a.Actor, id)
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	if page.CanRecord {
		w.Header().Set("X-FreeFSM-Office-Payment", "allowed")
	} else {
		w.Header().Set("X-FreeFSM-Office-Payment", "read-only")
	}
	_ = templates.CustomerReceipts(page, newOperationKey()).Render(r.Context(), w)
}

func reverseUnappliedPayment(w http.ResponseWriter, r *http.Request) {
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
	payment, err := parseUUID(chi.URLParam(r, "payment_id"), "Payment")
	if err != nil {
		http.Error(w, "invalid Payment", 400)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	_, err = settlement.New(a.Service.Pool).ReverseUnappliedPayment(r.Context(), a.Actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: r.FormValue("idempotency_key")}, ID: payment, CustomerID: id, EffectiveDate: settlement.Date(r.FormValue("date")), Reason: r.FormValue("reason")})
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/customers/%d/payments?flash=Receipt+reversed", id), 303)
}
