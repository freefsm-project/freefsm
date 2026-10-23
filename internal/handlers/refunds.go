package handlers

import (
	"encoding/json"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
	"net/http"
	"strconv"
)

func refundOperationResponse(w http.ResponseWriter, r *http.Request, key string) {
	a, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	result, err := settlement.New(a.Service.Pool).RefundReceipt(r.Context(), a.Actor, key)
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": result})
}
func refundOperationReceipt(w http.ResponseWriter, r *http.Request) {
	refundOperationResponse(w, r, chi.URLParam(r, "operation"))
}
func customerRefunds(w http.ResponseWriter, r *http.Request) {
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
	page, err := settlement.New(a.Service.Pool).RefundContext(r.Context(), a.Actor, id)
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	_ = templates.CustomerRefunds(page, newOperationKey()).Render(r.Context(), w)
}
