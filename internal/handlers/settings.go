package handlers

import (
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
)

type SettingsHandler struct {
	svc *services.CompanySettingsService
}

func NewSettingsHandler(svc *services.CompanySettingsService) *SettingsHandler {
	return &SettingsHandler{svc: svc}
}

func (h *SettingsHandler) Show(w http.ResponseWriter, r *http.Request) {
	cs, _ := h.svc.Get(r.Context())
	templates.SettingsPage(templates.SettingsPageData{
		Settings: cs,
		IsSetup:  r.URL.Path == "/setup/company",
	}).Render(r.Context(), w)
}

func (h *SettingsHandler) Save(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	dueDays, _ := strconv.Atoi(r.FormValue("default_due_days"))
	h.svc.Save(r.Context(), services.CompanySettingsParams{
		BusinessName:   r.FormValue("business_name"),
		Address:        r.FormValue("address"),
		City:           r.FormValue("city"),
		State:          r.FormValue("state"),
		Zip:            r.FormValue("zip"),
		Phone:          r.FormValue("phone"),
		Email:          r.FormValue("email"),
		TaxID:          r.FormValue("tax_id"),
		DefaultTaxRate: r.FormValue("default_tax_rate"),
		InvoicePrefix:  r.FormValue("invoice_prefix"),
		EstimatePrefix: r.FormValue("estimate_prefix"),
		DefaultDueDays: dueDays,
	})
	if r.URL.Path == "/setup/company" {
		http.Redirect(w, r, "/?flash=Setup+complete", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings?flash=Settings+saved", http.StatusSeeOther)
}

func companyName(cs *ent.CompanySettings) string {
	if cs == nil || cs.BusinessName == "" {
		return "FreeFSM"
	}
	return cs.BusinessName
}

func invoicePrefix(cs *ent.CompanySettings) string {
	if cs == nil || cs.InvoicePrefix == "" {
		return "INV-"
	}
	return cs.InvoicePrefix
}

func estimatePrefix(cs *ent.CompanySettings) string {
	if cs == nil || cs.EstimatePrefix == "" {
		return "EST-"
	}
	return cs.EstimatePrefix
}
