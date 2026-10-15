package handlers

import (
	"encoding/json"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"net/http"
	"strconv"
	"strings"
)

// This read-only selected-input projection returns only the effective rate,
// never the Customer's financial/master record or raw exemption flag.
func commercialTaxPreview(w http.ResponseWriter, r *http.Request) {
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		http.Error(w, "Unauthorized", 401)
		return
	}
	customer, _ := strconv.ParseInt(r.URL.Query().Get("customer_id"), 10, 64)
	job, _ := strconv.ParseInt(r.URL.Query().Get("job_id"), 10, 64)
	rate := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(r.URL.Query().Get("rate")), "%"))
	if rate == "" {
		rate = "0"
	}
	if _, err := commercial.Calculate(commercial.Input{TaxRate: rate}); err != nil {
		http.Error(w, "Enter a valid tax rate", 422)
		return
	}
	input, err := request.Service.DocumentCustomer(r.Context(), request.Actor, customer, job, rate)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"rate": input.TaxRate})
}
