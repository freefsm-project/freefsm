package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
)

var errUserCompanyRequired = errors.New("user company is required")

type PasswordHandler struct {
	userSvc     *services.UserService
	csSvc       *services.CompanySettingsService
	activitySvc *services.ActivityService
}

func NewPasswordHandler(userSvc *services.UserService, csSvc *services.CompanySettingsService, activitySvc *services.ActivityService) *PasswordHandler {
	return &PasswordHandler{userSvc: userSvc, csSvc: csSvc, activitySvc: activitySvc}
}

func (h *PasswordHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	user, _ := middleware.UserFromContext(r.Context())
	if user == nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	if r.Method == http.MethodGet {
		cs, err := h.csSvc.GetForCompany(r.Context(), user.CompanyID)
		if err != nil {
			internalServerError(w, r, "get password policy", err)
			return
		}
		templates.ChangePasswordPage(templates.ChangePasswordData{
			Error:          r.URL.Query().Get("error"),
			MinLength:      cs.PasswordMinLength,
			RequireUpper:   cs.PasswordRequireUppercase,
			RequireLower:   cs.PasswordRequireLowercase,
			RequireDigit:   cs.PasswordRequireDigit,
			RequireSpecial: cs.PasswordRequireSpecial,
		}).Render(r.Context(), w)
		return
	}

	r.ParseForm()
	current := r.FormValue("current_password")
	newPass := r.FormValue("new_password")
	if err := validatePasswordConfirmation(newPass, r.FormValue("confirm_password")); err != nil {
		http.Redirect(w, r, "/change-password?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	u, err := h.userSvc.GetByID(r.Context(), user.ID)
	if err != nil {
		internalServerError(w, r, "load current user", err)
		return
	}

	// Verify current password
	if err := h.userSvc.Authenticate(r.Context(), u.Email, current); err != nil {
		http.Redirect(w, r, "/change-password?error="+url.QueryEscape("current password incorrect"), http.StatusSeeOther)
		return
	}

	// Validate against company policy
	if u.CompanyID == nil {
		internalServerError(w, r, "get password policy", errUserCompanyRequired)
		return
	}
	cs, err := h.csSvc.GetForCompany(r.Context(), *u.CompanyID)
	if err != nil {
		internalServerError(w, r, "get password policy", err)
		return
	}
	if err := h.userSvc.ValidatePassword(newPass, cs); err != nil {
		http.Redirect(w, r, "/change-password?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	// Update password and clear flag
	if err := h.userSvc.SetPassword(r.Context(), user.ID, newPass); err != nil {
		internalServerError(w, r, "change password", err)
		return
	}
	if err := h.userSvc.ClearForcePasswordChange(r.Context(), user.ID); err != nil {
		slog.Error("clear force_password_change", "error", err)
	}

	if h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), user.CompanyID, user.ID, "password_changed", objectref.New(objectref.TypeUser, user.ID), map[string]interface{}{
			"entity_name": user.Name,
			"actor_name":  user.Name,
		})
	}

	http.Redirect(w, r, "/?flash=Password+changed", http.StatusSeeOther)
}
