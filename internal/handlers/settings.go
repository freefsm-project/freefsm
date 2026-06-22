package handlers

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
)

type SettingsHandler struct {
	svc         *services.CompanySettingsService
	emailSvc    *services.EmailService
	activitySvc *services.ActivityService
	entClient   *ent.Client
	uploadDir   string
}

func NewSettingsHandler(svc *services.CompanySettingsService, emailSvc *services.EmailService, activitySvc *services.ActivityService, entClient *ent.Client, uploadDir string) *SettingsHandler {
	return &SettingsHandler{svc: svc, emailSvc: emailSvc, activitySvc: activitySvc, entClient: entClient, uploadDir: uploadDir}
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
	if pwMinLen < 6 {
		http.Redirect(w, r, "/settings?flash=Password+minimum+length+must+be+at+least+6", http.StatusSeeOther)
		return
	}

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
		InvoiceColor:                r.FormValue("invoice_color"),
		InvoiceFooter:               r.FormValue("invoice_footer"),
		InvoiceLogoPath:             r.FormValue("invoice_logo_path"),
		InvoicePaymentTerms:         r.FormValue("invoice_payment_terms"),
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
			if oldSettings.InvoiceColor != newSettings.InvoiceColor {
				changed = append(changed, "invoice_color")
			}
			if oldSettings.InvoiceFooter != newSettings.InvoiceFooter {
				changed = append(changed, "invoice_footer")
			}
			if oldSettings.InvoiceLogoPath != newSettings.InvoiceLogoPath {
				changed = append(changed, "invoice_logo_path")
			}
			if oldSettings.InvoicePaymentTerms != newSettings.InvoicePaymentTerms {
				changed = append(changed, "invoice_payment_terms")
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

func (h *SettingsHandler) UploadInvoiceLogo(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		http.Error(w, "File too large", 413)
		return
	}
	defer r.MultipartForm.RemoveAll()

	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		http.Error(w, "No file", 400)
		return
	}

	fh := files[0]
	mimeType := fh.Header.Get("Content-Type")
	if !strings.HasPrefix(mimeType, "image/") {
		http.Error(w, "Only image files allowed", 400)
		return
	}

	f, err := fh.Open()
	if err != nil {
		http.Error(w, "Cannot open file", 500)
		return
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(fh.Filename))
	if ext == "" {
		ext = ".png"
	}
	logoPath := filepath.Join(h.uploadDir, "invoice-logo"+ext)

	dst, err := os.Create(logoPath)
	if err != nil {
		http.Error(w, "Cannot save file: "+err.Error(), 500)
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, f); err != nil {
		http.Error(w, "Cannot write file", 500)
		return
	}

	cs, err := h.svc.Get(r.Context())
	if err != nil {
		http.Error(w, "Cannot load settings", 500)
		return
	}

	if _, err := h.entClient.CompanySettings.UpdateOneID(cs.ID).SetInvoiceLogoPath(logoPath).Save(r.Context()); err != nil {
		http.Error(w, "Cannot update settings: "+err.Error(), 500)
		return
	}

	u, _ := middleware.UserFromContext(r.Context())
	if u != nil {
		h.activitySvc.Record(r.Context(), u.ID, "logo_uploaded", "company_settings", cs.ID, map[string]interface{}{
			"entity_name": "Company Settings",
			"actor_name":  u.Name,
		})
	}

	logoURL := "/settings/invoice-logo?t=" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	if logoPath != "" {
		fmt.Fprintf(w, `<input type="text" name="invoice_logo_path" value="%s" placeholder="File path" readonly style="font-family:monospace;font-size:0.85rem"/><div style="margin-top:0.5rem"><img src="%s" alt="Logo preview" style="max-height:64px;max-width:200px"/></div>`, logoPath, logoURL)
	} else {
		fmt.Fprintf(w, `<input type="text" name="invoice_logo_path" value="" placeholder="File path" readonly style="font-family:monospace;font-size:0.85rem"/>`)
	}
}

func (h *SettingsHandler) InvoiceLogo(w http.ResponseWriter, r *http.Request) {
	cs, err := h.svc.Get(r.Context())
	if err != nil || cs.InvoiceLogoPath == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	http.ServeFile(w, r, cs.InvoiceLogoPath)
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
