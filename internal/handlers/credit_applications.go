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

func creditApplications(w http.ResponseWriter, r *http.Request) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", 400)
		return
	}
	page, err := settlement.New(req.Service.Pool).ApplicationContext(r.Context(), req.Actor, id)
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	templates.CreditApplications(page, newOperationKey()).Render(r.Context(), w)
}

func applicationOperationReceipt(w http.ResponseWriter, r *http.Request) {
	writeApplicationReceipt(w, r, chi.URLParam(r, "operation"))
}

func writeApplicationReceipt(w http.ResponseWriter, r *http.Request, key string) {
	req, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	receipt, err := settlement.New(req.Service.Pool).ApplicationReceipt(r.Context(), req.Actor, key)
	if err != nil {
		writeSettlementError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": receipt})
}
