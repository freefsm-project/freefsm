package handlers

import (
	"errors"
	"io"
	"net/http"
	"net/url"
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
	uploadDir   string
}

func NewSettingsHandler(svc *services.CompanySettingsService, emailSvc *services.EmailService, activitySvc *services.ActivityService, uploadDir string) *SettingsHandler {
	return &SettingsHandler{svc: svc, emailSvc: emailSvc, activitySvc: activitySvc, uploadDir: uploadDir}
}

func (h *SettingsHandler) Show(w http.ResponseWriter, r *http.Request) {
	cs, _ := h.svc.Get(r.Context())
	render(w, r, templates.SettingsPage(templates.SettingsPageData{
		Settings: cs,
		IsSetup:  r.URL.Path == "/setup/company",
	}))
}

func (h *SettingsHandler) Save(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	dueDays, _ := strconv.Atoi(r.FormValue("default_due_days"))
	nextInvoiceNumber, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("next_invoice_number")), 10, 64)
	if nextInvoiceNumber < 1 {
		http.Redirect(w, r, "/settings?flash=Next+invoice+number+must+be+at+least+1", http.StatusSeeOther)
		return
	}
	smtpPort, _ := strconv.Atoi(r.FormValue("smtp_port"))
	pwMinLen, _ := strconv.Atoi(r.FormValue("password_min_length"))
	if pwMinLen < 6 {
		http.Redirect(w, r, "/settings?flash=Password+minimum+length+must+be+at+least+6", http.StatusSeeOther)
		return
	}

	oldSettings, _ := h.svc.Get(r.Context())
	mapTileURL := strings.TrimSpace(r.FormValue("map_tile_url"))
	geocoderURL := strings.TrimRight(strings.TrimSpace(r.FormValue("geocoder_url")), "/")
	if r.URL.Path == "/setup/company" && oldSettings != nil {
		mapTileURL = oldSettings.MapTileURL
		geocoderURL = oldSettings.GeocoderURL
	}
	if mapTileURL != "" {
		if parsed, err := url.Parse(mapTileURLSample(mapTileURL)); err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			http.Redirect(w, r, "/settings?flash=Map+tile+URL+must+be+a+valid+http(s)+URL", http.StatusSeeOther)
			return
		}
	}
	if geocoderURL != "" {
		if parsed, err := url.Parse(geocoderURL); err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			http.Redirect(w, r, "/settings?flash=Geocoder+URL+must+be+a+valid+http(s)+URL", http.StatusSeeOther)
			return
		}
	}

	err := h.svc.Save(r.Context(), services.CompanySettingsParams{
		BusinessName:                r.FormValue("business_name"),
		Address:                     r.FormValue("address"),
		City:                        r.FormValue("city"),
		State:                       r.FormValue("state"),
		Zip:                         r.FormValue("zip"),
		Phone:                       r.FormValue("phone"),
		Email:                       r.FormValue("email"),
		TaxID:                       r.FormValue("tax_id"),
		DefaultTaxRate:              r.FormValue("default_tax_rate"),
		InvoicePrefix:               r.FormValue("invoice_prefix"),
		NextInvoiceNumber:           nextInvoiceNumber,
		EstimatePrefix:              r.FormValue("estimate_prefix"),
		DefaultDueDays:              dueDays,
		SmtpHost:                    r.FormValue("smtp_host"),
		SmtpPort:                    smtpPort,
		SmtpUser:                    r.FormValue("smtp_user"),
		SmtpPassword:                r.FormValue("smtp_password"),
		SmtpFrom:                    r.FormValue("smtp_from"),
		EmailAutoCC:                 r.FormValue("email_auto_cc"),
		InvoiceEmailSubject:         r.FormValue("invoice_email_subject"),
		InvoiceEmailBody:            r.FormValue("invoice_email_body"),
		EstimateEmailSubject:        r.FormValue("estimate_email_subject"),
		EstimateEmailBody:           r.FormValue("estimate_email_body"),
		Timezone:                    r.FormValue("timezone"),
		PasswordMinLength:           pwMinLen,
		PasswordRequireUppercase:    r.FormValue("password_require_uppercase") == "on",
		PasswordRequireLowercase:    r.FormValue("password_require_lowercase") == "on",
		PasswordRequireDigit:        r.FormValue("password_require_digit") == "on",
		PasswordRequireSpecial:      r.FormValue("password_require_special") == "on",
		InvoiceColor:                r.FormValue("invoice_color"),
		InvoiceFooter:               r.FormValue("invoice_footer"),
		InvoicePaymentTerms:         r.FormValue("invoice_payment_terms"),
		PDFShowLineItemDescriptions: r.FormValue("pdf_show_line_item_descriptions") == "on",
		MapTileURL:                  mapTileURL,
		GeocoderURL:                 geocoderURL,
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
			if oldSettings.NextInvoiceNumber != newSettings.NextInvoiceNumber {
				changed = append(changed, "next_invoice_number")
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
			if oldSettings.EmailAutoCc != newSettings.EmailAutoCc {
				changed = append(changed, "email_auto_cc")
			}
			if oldSettings.InvoiceEmailSubject != newSettings.InvoiceEmailSubject {
				changed = append(changed, "invoice_email_subject")
			}
			if oldSettings.InvoiceEmailBody != newSettings.InvoiceEmailBody {
				changed = append(changed, "invoice_email_body")
			}
			if oldSettings.EstimateEmailSubject != newSettings.EstimateEmailSubject {
				changed = append(changed, "estimate_email_subject")
			}
			if oldSettings.EstimateEmailBody != newSettings.EstimateEmailBody {
				changed = append(changed, "estimate_email_body")
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
			if oldSettings.PdfShowLineItemDescriptions != newSettings.PdfShowLineItemDescriptions {
				changed = append(changed, "pdf_show_line_item_descriptions")
			}
			if oldSettings.MapTileURL != newSettings.MapTileURL {
				changed = append(changed, "map_tile_url")
			}
			if oldSettings.GeocoderURL != newSettings.GeocoderURL {
				changed = append(changed, "geocoder_url")
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

func mapTileURLSample(tileURL string) string {
	return strings.NewReplacer("{s}", "a", "{z}", "0", "{x}", "0", "{y}", "0", "{r}", "").Replace(tileURL)
}

func (h *SettingsHandler) TestEmail(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", 401)
		return
	}

	if err := h.emailSvc.SendTestEmail(r.Context(), u.Email, u.Name); err != nil {
		render(w, r, templates.TestEmailResult(false, err.Error()))
		return
	}
	render(w, r, templates.TestEmailResult(true, ""))
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

	f, err := files[0].Open()
	if err != nil {
		http.Error(w, "Cannot open file", 500)
		return
	}
	defer f.Close()

	ext, err := detectInvoiceLogoExt(f)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
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

	cs, err := h.svc.UpdateInvoiceLogoPath(r.Context(), logoPath)
	if err != nil {
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

	render(w, r, templates.InvoiceLogoPreview(logoPath, strconv.FormatInt(time.Now().UnixMilli(), 10)))
}

func detectInvoiceLogoExt(f io.ReadSeeker) (string, error) {
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	switch http.DetectContentType(buf[:n]) {
	case "image/png":
		return ".png", nil
	case "image/jpeg":
		return ".jpg", nil
	default:
		return "", errors.New("Only PNG and JPEG images allowed")
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
