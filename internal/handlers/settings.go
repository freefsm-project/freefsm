package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
)

type SettingsHandler struct {
	svc         *services.CompanySettingsService
	emailSvc    *services.EmailService
	activitySvc *services.ActivityService
}

func NewSettingsHandler(svc *services.CompanySettingsService, emailSvc *services.EmailService, activitySvc *services.ActivityService) *SettingsHandler {
	return &SettingsHandler{svc: svc, emailSvc: emailSvc, activitySvc: activitySvc}
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
	smtpPort, _ := strconv.Atoi(r.FormValue("smtp_port"))
	pwMinLen, _ := strconv.Atoi(r.FormValue("password_min_length"))

	oldSettings, _ := h.svc.Get(r.Context())

	err := h.svc.Save(r.Context(), services.CompanySettingsParams{
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
		SmtpHost:       r.FormValue("smtp_host"),
		SmtpPort:       smtpPort,
		SmtpUser:       r.FormValue("smtp_user"),
		SmtpPassword:   r.FormValue("smtp_password"),
		SmtpFrom:       r.FormValue("smtp_from"),
		Timezone:       r.FormValue("timezone"),
		PasswordMinLength:         pwMinLen,
		PasswordRequireUppercase:  r.FormValue("password_require_uppercase") == "on",
		PasswordRequireLowercase:  r.FormValue("password_require_lowercase") == "on",
		PasswordRequireDigit:        r.FormValue("password_require_digit") == "on",
		PasswordRequireSpecial:      r.FormValue("password_require_special") == "on",
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	u, _ := middleware.UserFromContext(r.Context())
	if u != nil && oldSettings != nil {
		changed := []string{}
		newSettings, _ := h.svc.Get(r.Context())
		if newSettings != nil {
			if oldSettings.BusinessName != newSettings.BusinessName {
				changed = append(changed, "business_name")
			}
			if oldSettings.Address != newSettings.Address {
				changed = append(changed, "address")
			}
			if oldSettings.City != newSettings.City {
				changed = append(changed, "city")
			}
			if oldSettings.State != newSettings.State {
				changed = append(changed, "state")
			}
			if oldSettings.Zip != newSettings.Zip {
				changed = append(changed, "zip")
			}
			if oldSettings.Phone != newSettings.Phone {
				changed = append(changed, "phone")
			}
			if oldSettings.Email != newSettings.Email {
				changed = append(changed, "email")
			}
			if oldSettings.TaxID != newSettings.TaxID {
				changed = append(changed, "tax_id")
			}
			if oldSettings.DefaultTaxRate != newSettings.DefaultTaxRate {
				changed = append(changed, "default_tax_rate")
			}
			if oldSettings.InvoicePrefix != newSettings.InvoicePrefix {
				changed = append(changed, "invoice_prefix")
			}
			if oldSettings.EstimatePrefix != newSettings.EstimatePrefix {
				changed = append(changed, "estimate_prefix")
			}
			if oldSettings.DefaultDueDays != newSettings.DefaultDueDays {
				changed = append(changed, "default_due_days")
			}
			if oldSettings.SMTPHost != newSettings.SMTPHost {
				changed = append(changed, "smtp_host")
			}
			if oldSettings.SMTPPort != newSettings.SMTPPort {
				changed = append(changed, "smtp_port")
			}
			if oldSettings.SMTPUser != newSettings.SMTPUser {
				changed = append(changed, "smtp_user")
			}
			if oldSettings.SMTPPassword != newSettings.SMTPPassword {
				changed = append(changed, "smtp_password")
			}
			if oldSettings.SMTPFrom != newSettings.SMTPFrom {
				changed = append(changed, "smtp_from")
			}
			if oldSettings.Timezone != newSettings.Timezone {
				changed = append(changed, "timezone")
			}
			if oldSettings.PasswordMinLength != newSettings.PasswordMinLength {
				changed = append(changed, "password_min_length")
			}
			if oldSettings.PasswordRequireUppercase != newSettings.PasswordRequireUppercase {
				changed = append(changed, "password_require_uppercase")
			}
			if oldSettings.PasswordRequireLowercase != newSettings.PasswordRequireLowercase {
				changed = append(changed, "password_require_lowercase")
			}
			if oldSettings.PasswordRequireDigit != newSettings.PasswordRequireDigit {
				changed = append(changed, "password_require_digit")
			}
			if oldSettings.PasswordRequireSpecial != newSettings.PasswordRequireSpecial {
				changed = append(changed, "password_require_special")
			}
			if len(changed) > 0 {
				h.activitySvc.Record(r.Context(), u.ID, "settings_updated", "company_settings", newSettings.ID, map[string]interface{}{
					"entity_name": "Company Settings",
					"actor_name":  u.Name,
					"changed":     strings.Join(changed, ", "),
				})
			}
		}
	}

	if r.URL.Path == "/setup/company" {
		http.Redirect(w, r, "/?flash=Setup+complete", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings?flash=Settings+saved", http.StatusSeeOther)
}

func (h *SettingsHandler) TestEmail(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

	if err := h.emailSvc.SendTestEmail(r.Context(), u.Email, u.Name); err != nil {
		templates.TestEmailResult(false, err.Error()).Render(r.Context(), w)
		return
	}
	templates.TestEmailResult(true, "").Render(r.Context(), w)
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
