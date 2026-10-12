package handlers

import (
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type SettingsHandler struct {
	authority   *authorization.Service
	control     *instancecontrol.Control
	users       *services.UserService
	svc         *services.CompanySettingsService
	emailSvc    *services.EmailService
	activitySvc *services.ActivityService
	uploadDir   string
}

func NewSettingsHandler(svc *services.CompanySettingsService, emailSvc *services.EmailService, activitySvc *services.ActivityService, uploadDir string) *SettingsHandler {
	return &SettingsHandler{svc: svc, emailSvc: emailSvc, activitySvc: activitySvc, uploadDir: uploadDir}
}

func (h *SettingsHandler) Show(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/settings" || r.URL.Path == "/setup/company" {
		view, err := h.authority.Configuration(r.Context(), authorizationActor(r))
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		view.EmailDisabled = h.control != nil && h.control.EmailDisabled()
		if r.URL.Path == "/setup/company" {
			view.FormAction = "/setup/company"
			for _, section := range view.Sections {
				if section.Name == "company" {
					view.Sections = []authorization.ConfigurationSection{section}
					break
				}
			}
		}
		render(w, r, templates.CompanyConfiguration(view, ""))
		return
	}
	cs, err := h.svc.Get(r.Context())
	if err != nil {
		http.Error(w, "Settings unavailable", http.StatusServiceUnavailable)
		return
	}
	var recoveryReviewCount int
	if h.authority != nil {
		p, err := h.authority.Administrator(r.Context(), authorizationActor(r))
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		if err = h.authority.Pool.QueryRow(r.Context(), `SELECT count(*) FROM password_recovery_review WHERE company_id=$1`, p.CompanyID).Scan(&recoveryReviewCount); err != nil {
			internalServerError(w, r, "read recovery review", err)
			return
		}
	}
	render(w, r, templates.SettingsPage(templates.SettingsPageData{
		RecoveryReviewCount: recoveryReviewCount,
		Settings:            cs,
		IsSetup:             r.URL.Path == "/setup/company",
		EmailDisabled:       h.control != nil && h.control.EmailDisabled(),
	}))
}

func (h *SettingsHandler) Save(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/settings" || r.URL.Path == "/setup/company" {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		revision, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
		err := h.authority.SaveConfiguration(r.Context(), authorizationActor(r), r.FormValue("section"), revision, r.PostForm)
		if errors.Is(err, authorization.ErrConfigurationInput) {
			view, loadErr := h.authority.Configuration(r.Context(), authorizationActor(r))
			if loadErr != nil {
				authorizationError(w, r, loadErr)
				return
			}
			for i := range view.Sections {
				if view.Sections[i].Name == r.FormValue("section") {
					for _, f := range view.Sections[i].Fields {
						if r.PostForm.Has(f.Name) {
							view.Sections[i].Values[f.Name] = r.PostForm.Get(f.Name)
						}
					}
				}
			}
			view.Revision = revision
			w.WriteHeader(422)
			render(w, r, templates.CompanyConfiguration(view, err.Error()))
			return
		}
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		if r.URL.Path == "/setup/company" {
			http.Redirect(w, r, "/?flash=Setup+complete", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/settings?flash=Settings+saved", http.StatusSeeOther)
		return
	}
	r.ParseForm()
	smtpPort, _ := strconv.Atoi(r.FormValue("smtp_port"))
	pwMinLen, _ := strconv.Atoi(r.FormValue("password_min_length"))
	if pwMinLen < 6 || pwMinLen > 72 {
		http.Redirect(w, r, "/settings/advanced?flash=Password+minimum+length+must+be+between+6+and+72", http.StatusSeeOther)
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

	err := h.svc.SaveAdministrativeSettings(r.Context(), services.AdministrativeSettingsParams{
		SmtpHost:                 r.FormValue("smtp_host"),
		SmtpPort:                 smtpPort,
		SmtpUser:                 r.FormValue("smtp_user"),
		SmtpPassword:             r.FormValue("smtp_password"),
		SmtpFrom:                 r.FormValue("smtp_from"),
		EmailAutoCC:              r.FormValue("email_auto_cc"),
		InvoiceEmailSubject:      r.FormValue("invoice_email_subject"),
		InvoiceEmailBody:         r.FormValue("invoice_email_body"),
		EstimateEmailSubject:     r.FormValue("estimate_email_subject"),
		EstimateEmailBody:        r.FormValue("estimate_email_body"),
		PasswordMinLength:        pwMinLen,
		PasswordRequireUppercase: r.FormValue("password_require_uppercase") == "on",
		PasswordRequireLowercase: r.FormValue("password_require_lowercase") == "on",
		PasswordRequireDigit:     r.FormValue("password_require_digit") == "on",
		PasswordRequireSpecial:   r.FormValue("password_require_special") == "on",
		MapTileURL:               mapTileURL,
		GeocoderURL:              geocoderURL,
		EmailTrackingEnabled:     r.FormValue("email_tracking_enabled") == "on",
	})
	if err != nil {
		internalServerError(w, r, "save administrative settings", err)
		return
	}

	u, _ := middleware.UserFromContext(r.Context())
	if u != nil && oldSettings != nil {
		changed := []string{}
		newSettings, _ := h.svc.Get(r.Context())
		if newSettings != nil {
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
			if oldSettings.MapTileURL != newSettings.MapTileURL {
				changed = append(changed, "map_tile_url")
			}
			if oldSettings.GeocoderURL != newSettings.GeocoderURL {
				changed = append(changed, "geocoder_url")
			}
			if oldSettings.EmailTrackingEnabled != newSettings.EmailTrackingEnabled {
				changed = append(changed, "email_tracking_enabled")
			}
			if len(changed) > 0 {
				h.activitySvc.Record(r.Context(), u.CompanyID, u.ID, "settings_updated", objectref.New(objectref.TypeCompanySettings, newSettings.ID), map[string]interface{}{
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

func (h *SettingsHandler) ReviewNumber(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form", 400)
		return
	}
	revision, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	number, _ := strconv.ParseInt(r.FormValue("number"), 10, 64)
	next, _ := strconv.ParseInt(r.FormValue("next_number"), 10, 64)
	if r.FormValue("confirmation") != "REVIEWED IDENTITY" {
		http.Error(w, "Explicit review required", 422)
		return
	}
	if err := h.authority.ReviewLegacyNumber(r.Context(), authorizationActor(r), revision, r.FormValue("type"), id, number, next, r.FormValue("label")); err != nil {
		authorizationError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?flash=Number+reviewed", 303)
}

func (h *SettingsHandler) CustomerDefaults(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		revision, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
		if err := h.authority.SaveCustomerDefaults(r.Context(), authorizationActor(r), id, revision, r.PostForm); err != nil {
			authorizationError(w, r, err)
			return
		}
		http.Redirect(w, r, r.URL.Path+"?flash=Defaults+saved", 303)
		return
	}
	view, err := h.authority.CustomerDefaults(r.Context(), authorizationActor(r), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	render(w, r, templates.CustomerDocumentDefaults(view))
}

func mapTileURLSample(tileURL string) string {
	return strings.NewReplacer("{s}", "a", "{z}", "0", "{x}", "0", "{y}", "0", "{r}", "").Replace(tileURL)
}

func (h *SettingsHandler) TestEmail(w http.ResponseWriter, r *http.Request) {
	if err := h.emailSvc.CheckAvailability(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		render(w, r, templates.TestEmailResult(false, "Outbound email is disabled or unavailable. An administrator must explicitly enable it in Settings."))
		return
	}
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

func (h *SettingsHandler) EnableEmail(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil || !u.ProtectedAdministrator {
		http.Error(w, "Forbidden", 403)
		return
	}
	if h.users == nil {
		http.Error(w, "Administrator verification unavailable", 503)
		return
	}
	active, err := h.users.GetByID(r.Context(), u.ID)
	if err != nil || !active.IsActive || active.AuthorizationRoleID == nil || *active.AuthorizationRoleID != 1 || active.ForcePasswordChange {
		http.Error(w, "Forbidden", 403)
		return
	}
	if h.control == nil || h.control.EnableEmail() != nil {
		http.Error(w, "Email could not be enabled", 503)
		return
	}
	http.Redirect(w, r, "/settings?flash=Outbound+email+enabled", http.StatusSeeOther)
}

func (h *SettingsHandler) UploadInvoiceLogo(w http.ResponseWriter, r *http.Request) {
	p, err := h.authority.Principal(r.Context(), authorizationActor(r))
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if !p.Allows("configuration.company.manage", authorization.Configuration) {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 25<<20)
	if err := r.ParseMultipartForm(25 << 20); err != nil {
		http.Error(w, "File too large", 413)
		return
	}
	defer r.MultipartForm.RemoveAll()
	for name, values := range r.MultipartForm.Value {
		if (name != "revision" && name != "csrf_token") || len(values) != 1 {
			http.Error(w, "Invalid logo form", 400)
			return
		}
	}

	files := r.MultipartForm.File["file"]
	if len(files) != 1 || len(r.MultipartForm.File) != 1 {
		http.Error(w, "Choose exactly one logo", 400)
		return
	}
	if files[0].Size > 25<<20 {
		http.Error(w, "Logo is too large", 413)
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
	config, _, err := image.DecodeConfig(f)
	if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 25000000 {
		http.Error(w, "Invalid or oversized image", 400)
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "Cannot read image", 500)
		return
	}
	if _, _, err = image.Decode(f); err != nil {
		http.Error(w, "Invalid image data", 400)
		return
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		http.Error(w, "Cannot read image", 500)
		return
	}
	dst, err := os.CreateTemp(h.uploadDir, "invoice-logo-*"+ext)
	if err != nil {
		internalServerError(w, r, "persist company logo", err)
		return
	}
	logoPath := dst.Name()
	published := false
	defer func() {
		dst.Close()
		if !published {
			os.Remove(logoPath)
		}
	}()

	if _, err := io.Copy(dst, f); err != nil {
		http.Error(w, "Cannot write file", 500)
		return
	}

	if err = dst.Sync(); err == nil {
		err = dst.Close()
	}
	if err != nil {
		http.Error(w, "Cannot persist image", 500)
		return
	}
	dir, err := os.Open(h.uploadDir)
	if err == nil {
		err = dir.Sync()
		dir.Close()
	}
	if err != nil {
		http.Error(w, "Cannot persist image directory", 500)
		return
	}
	revision, _ := strconv.ParseInt(r.FormValue("revision"), 10, 64)
	if err = h.authority.SaveCompanyLogo(r.Context(), authorizationActor(r), revision, logoPath); err != nil {
		authorizationError(w, r, err)
		return
	}
	published = true
	http.Redirect(w, r, "/settings?flash=Logo+saved", http.StatusSeeOther)
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
	p, err := h.authority.Principal(r.Context(), authorizationActor(r))
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	if !p.Allows("configuration.company.view", authorization.Configuration) {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
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
	if cs == nil {
		return "INV-"
	}
	return cs.InvoicePrefix
}

func estimatePrefix(cs *ent.CompanySettings) string {
	if cs == nil {
		return "EST-"
	}
	return cs.EstimatePrefix
}
