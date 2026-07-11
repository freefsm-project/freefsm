package handlers

import (
	"log/slog"
	"net/http"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
)

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
		cs, _ := h.csSvc.Get(r.Context())
		templates.ChangePasswordPage(templates.ChangePasswordData{
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
	confirm := r.FormValue("confirm_password")

	if newPass != confirm {
		http.Redirect(w, r, "/change-password?error=passwords+do+not+match", http.StatusSeeOther)
		return
	}

	u, err := h.userSvc.GetByID(r.Context(), user.ID)
	if err != nil {
		internalServerError(w, r, "load current user", err)
		return
	}

	// Verify current password
	if err := h.userSvc.Authenticate(r.Context(), u.Email, current); err != nil {
		http.Redirect(w, r, "/change-password?error=current+password+incorrect", http.StatusSeeOther)
		return
	}

	// Validate against company policy
	cs, _ := h.csSvc.Get(r.Context())
	if err := h.userSvc.ValidatePassword(newPass, cs); err != nil {
		http.Redirect(w, r, "/change-password?error="+err.Error(), http.StatusSeeOther)
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

	if user != nil {
		h.activitySvc.Record(r.Context(), user.ID, "password_changed", objectref.New(objectref.TypeUser, user.ID), map[string]interface{}{
			"entity_name": user.Name,
			"actor_name":  user.Name,
		})
	}

	http.Redirect(w, r, "/?flash=Password+changed", http.StatusSeeOther)
}
