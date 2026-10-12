package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type UserHandler struct {
	svc         *services.UserService
	emailSvc    *services.EmailService
	inviteSvc   *services.InvitationService
	resetSvc    *services.PasswordResetService
	csSvc       *services.CompanySettingsService
	activitySvc *services.ActivityService
	cfg         *config.Config
}

func NewUserHandler(svc *services.UserService, emailSvc *services.EmailService, inviteSvc *services.InvitationService, csSvc *services.CompanySettingsService, activitySvc *services.ActivityService, cfg *config.Config) *UserHandler {
	return &UserHandler{svc: svc, emailSvc: emailSvc, inviteSvc: inviteSvc, csSvc: csSvc, activitySvc: activitySvc, cfg: cfg}
}

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	users, err := h.svc.ListAll(r.Context())
	if err != nil {
		internalServerError(w, r, "list users", err)
		return
	}
	roleNames := map[int64]string{}
	if request, ok := authorization.FromContext(r.Context()); ok {
		roleNames, err = request.Service.AccountRoleNames(r.Context(), request.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
	}
	rows := make([]templates.UserRow, len(users))
	for i, u := range users {
		rows[i] = templates.UserRow{
			ID: u.ID, Name: u.Name, Email: u.Email,
			Role: u.Role, IsActive: u.IsActive,
		}
		if name, ok := roleNames[u.ID]; ok {
			rows[i].Role = name
		}
	}
	templates.UsersIndex(templates.UserListData{Users: rows}).Render(r.Context(), w)
}

func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		var options []templates.SelectOption
		if request, ok := authorization.FromContext(r.Context()); ok {
			review, err := request.Service.Review(r.Context(), request.Actor)
			if err != nil {
				authorizationError(w, r, err)
				return
			}
			for _, role := range review.Roles {
				options = append(options, templates.SelectOption{Value: role.ID, Label: role.Name})
			}
		}
		templates.UserForm(templates.UserFormData{
			IsNew:       true,
			User:        &templates.UserDetail{User: templates.UserRow{Role: "tech"}},
			Roles:       []string{"admin", "dispatcher", "tech"},
			RoleOptions: options,
		}).Render(r.Context(), w)
		return
	}
	r.ParseForm()
	a, ok := middleware.UserFromContext(r.Context())
	if !ok || a == nil || a.CompanyID <= 0 {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	sendWelcome := r.FormValue("send_welcome_email") == "on"
	if sendWelcome {
		if err := h.invitationMailAvailable(r); err != nil {
			http.Redirect(w, r, "/users/new?flash="+url.QueryEscape("Email unavailable. Use manual onboarding with a temporary password, or configure mail and try again. "+err.Error()), http.StatusSeeOther)
			return
		}
	}
	password := r.FormValue("password")
	if sendWelcome {
		password = ""
	}
	if !sendWelcome {
		if err := validatePasswordConfirmation(password, r.FormValue("confirm_password")); err != nil {
			http.Redirect(w, r, "/users/new?flash="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
		cs, err := h.csSvc.GetForCompany(r.Context(), a.CompanyID)
		if err != nil {
			internalServerError(w, r, "get company password policy", err)
			return
		}
		if err := h.svc.ValidatePassword(password, cs); err != nil {
			http.Redirect(w, r, "/users/new?flash="+url.QueryEscape(err.Error()), http.StatusSeeOther)
			return
		}
	}
	roleID, _ := strconv.ParseInt(r.FormValue("role_id"), 10, 64)
	result, err := h.svc.Create(r.Context(), services.UserCreateParams{
		CompanyID:        a.CompanyID,
		Name:             r.FormValue("name"),
		Email:            r.FormValue("email"),
		Password:         password,
		Role:             r.FormValue("role"),
		RoleID:           roleID,
		SendWelcomeEmail: sendWelcome,
	})
	if err != nil {
		internalServerError(w, r, "create user", err)
		return
	}

	if a != nil && h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), a.CompanyID, a.ID, "user_created", objectref.New(objectref.TypeUser, result.ID), map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  a.Name,
		})
	}

	if sendWelcome {
		token, err := h.inviteSvc.CreateInvite(r.Context(), a.CompanyID, result.ID)
		if err != nil {
			http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=", result.ID)+url.QueryEscape("Account pending; invitation could not be prepared. Use Resend Welcome on this account."), http.StatusSeeOther)
			return
		}
		inviteURL := absoluteAppURL(h.cfg, r, "/accept-invite?token="+url.QueryEscape(token))
		if err := h.emailSvc.SendWelcomeEmail(r.Context(), result.Email, result.Name, inviteURL); err != nil {
			slog.Error("send welcome email", "error", err, "user", result.Email)
			http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=", result.ID)+url.QueryEscape("Account pending; welcome email was not accepted. Check mail settings and resend using Resend Welcome on this account."), http.StatusSeeOther)
			return
		}
		if err := h.inviteSvc.RecordMailAccepted(r.Context(), a.CompanyID, result.ID); err != nil {
			http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=", result.ID)+url.QueryEscape("Mail transport accepted the invitation, but recording confirmation failed. Account remains pending; check before resending."), http.StatusSeeOther)
			return
		}
		if a != nil && h.activitySvc != nil {
			h.activitySvc.Record(r.Context(), a.CompanyID, a.ID, "welcome_invite_sent", objectref.New(objectref.TypeUser, result.ID), map[string]interface{}{
				"entity_name": result.Name,
				"actor_name":  a.Name,
			})
		}
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=", result.ID)+url.QueryEscape("Account pending; mail transport accepted the invitation. This does not confirm inbox delivery."), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/users?flash="+url.QueryEscape("Manual onboarding completed. The user must replace the temporary password before normal access."), http.StatusSeeOther)
}

func (h *UserHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	a, ok := middleware.UserFromContext(r.Context())
	if !ok || a == nil {
		http.NotFound(w, r)
		return
	}
	u, err := h.svc.GetByID(r.Context(), id)
	if err != nil || u.CompanyID == nil || *u.CompanyID != a.CompanyID {
		http.NotFound(w, r)
		return
	}
	canResend, err := h.inviteSvc.CanResendWelcome(r.Context(), a.CompanyID, id)
	if err != nil {
		internalServerError(w, r, "check welcome resend eligibility", err)
		return
	}
	row := userToRow(u)
	if request, ok := authorization.FromContext(r.Context()); ok {
		names, err := request.Service.AccountRoleNames(r.Context(), request.Actor)
		if err != nil {
			authorizationError(w, r, err)
			return
		}
		row.Role = names[u.ID]
	}
	review, err := h.inviteSvc.NeedsOnboardingReview(r.Context(), a.CompanyID, id)
	if err != nil {
		internalServerError(w, r, "load onboarding review", err)
		return
	}
	templates.UserShow(templates.UserDetailPage{User: row, CanResendWelcome: canResend, OnboardingReview: review}).Render(r.Context(), w)
}

func (h *UserHandler) Update(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if r.Method == http.MethodGet {
		u, err := h.svc.GetByID(r.Context(), id)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		templates.UserForm(templates.UserFormData{
			IsNew: false, User: &templates.UserDetail{User: userToRow(u)},
			Roles: []string{"admin", "dispatcher", "tech"},
		}).Render(r.Context(), w)
		return
	}
	r.ParseForm()
	if request, ok := authorization.FromContext(r.Context()); ok {
		if r.Form.Has("role") || r.Form.Has("role_id") || r.Form.Has("is_active") || r.Form.Has("password") {
			authorizationError(w, r, authorization.ErrConflict)
			return
		}
		if err := request.Service.EditIdentity(r.Context(), request.Actor, id, r.FormValue("name"), r.FormValue("email")); err != nil {
			authorizationError(w, r, err)
			return
		}
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=User+updated", id), http.StatusSeeOther)
		return
	}
	_, err := h.svc.Update(r.Context(), id, services.UserUpdateParams{
		Name:  formPtr(r.FormValue("name")),
		Email: formPtr(r.FormValue("email")),
		Role:  formPtr(r.FormValue("role")),
	})
	if err != nil {
		internalServerError(w, r, "update user", err)
		return
	}

	a, _ := middleware.UserFromContext(r.Context())
	if a != nil && h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), a.CompanyID, a.ID, "user_updated", objectref.New(objectref.TypeUser, id), map[string]interface{}{
			"entity_name": r.FormValue("name"),
			"actor_name":  a.Name,
		})
	}

	http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=User+updated", id), http.StatusSeeOther)
}

func (h *UserHandler) Disable(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	user, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	newState := !user.IsActive
	if !user.IsActive && user.OnboardingCompletedAt == nil {
		// Pending activation is not an enabled/disabled toggle. Only redemption
		// of a fresh invitation may activate this account.
		newState = false
	}
	if err := h.svc.SetActive(r.Context(), id, newState); err != nil {
		authorizationError(w, r, err)
		return
	}

	a, _ := middleware.UserFromContext(r.Context())
	if a != nil && h.activitySvc != nil {
		action := "user_disabled"
		if newState {
			action = "user_enabled"
		}
		h.activitySvc.Record(r.Context(), a.CompanyID, a.ID, action, objectref.New(objectref.TypeUser, id), map[string]interface{}{
			"entity_name": user.Name,
			"actor_name":  a.Name,
		})
	}

	http.Redirect(w, r, "/users?flash=User+toggled", http.StatusSeeOther)
}

func (h *UserHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	r.ParseForm()
	a, ok := middleware.UserFromContext(r.Context())
	if !ok || a == nil {
		http.NotFound(w, r)
		return
	}
	target, err := h.svc.GetByID(r.Context(), id)
	if err != nil || target.CompanyID == nil || *target.CompanyID != a.CompanyID {
		http.NotFound(w, r)
		return
	}
	password := r.FormValue("password")
	if err := validatePasswordConfirmation(password, r.FormValue("confirm_password")); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=%s", id, url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}
	cs, err := h.csSvc.GetForCompany(r.Context(), *target.CompanyID)
	if err != nil {
		internalServerError(w, r, "get target company password policy", err)
		return
	}
	if err := h.svc.ValidatePassword(password, cs); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=%s", id, url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}
	if err := h.svc.ResetPasswordAsAdministrator(r.Context(), id, password); err != nil {
		internalServerError(w, r, "admin reset password", err)
		return
	}

	if h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), a.CompanyID, a.ID, "password_reset", objectref.New(objectref.TypeUser, id), map[string]interface{}{
			"entity_name": target.Name,
			"actor_name":  a.Name,
		})
	}

	http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=Password+reset", id), http.StatusSeeOther)
}

func (h *UserHandler) Preferences(w http.ResponseWriter, r *http.Request) {
	u, ok := middleware.UserFromContext(r.Context())
	if !ok || u == nil {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	if r.Method == http.MethodGet {
		templates.PreferencesPage(templates.PreferencesData{FontSize: normalizedFontSize(u.FontSize)}).Render(r.Context(), w)
		return
	}

	r.ParseForm()
	fontSize := normalizedFontSize(r.FormValue("font_size"))
	if err := h.svc.UpdateFontSize(r.Context(), u.ID, fontSize); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, "/preferences?flash=Preferences+saved", http.StatusSeeOther)
}

func (h *UserHandler) ResendWelcome(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	a, ok := middleware.UserFromContext(r.Context())
	if !ok || a == nil {
		http.Error(w, "welcome invitation cannot be resent", http.StatusConflict)
		return
	}
	eligible, err := h.inviteSvc.CanResendWelcome(r.Context(), a.CompanyID, id)
	if err != nil {
		internalServerError(w, r, "check pending invitation", err)
		return
	}
	if !eligible {
		http.Error(w, "welcome invitation cannot be resent", http.StatusConflict)
		return
	}
	if err := h.invitationMailAvailable(r); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=", id)+url.QueryEscape("Email unavailable. Configure mail and resend this pending invitation. "+err.Error()), http.StatusSeeOther)
		return
	}
	user, token, err := h.inviteSvc.RenewPendingInvite(r.Context(), a.CompanyID, id)
	if err != nil {
		if errors.Is(err, services.ErrWelcomeResendIneligible) {
			http.Error(w, "welcome invitation cannot be resent", http.StatusConflict)
			return
		}
		internalServerError(w, r, "renew welcome invitation", err)
		return
	}

	inviteURL := absoluteAppURL(h.cfg, r, "/accept-invite?token="+url.QueryEscape(token))
	if err := h.emailSvc.SendWelcomeEmail(r.Context(), user.Email, user.Name, inviteURL); err != nil {
		slog.Error("resend welcome email", "error", err, "user", user.Email)
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=", id)+url.QueryEscape("Account pending; welcome email was not accepted. Check mail settings and resend using Resend Welcome."), http.StatusSeeOther)
		return
	}
	if err := h.inviteSvc.RecordMailAccepted(r.Context(), a.CompanyID, id); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=", id)+url.QueryEscape("Mail transport accepted the invitation, but recording confirmation failed. Check before resending."), http.StatusSeeOther)
		return
	}
	if h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), a.CompanyID, a.ID, "welcome_resent", objectref.New(objectref.TypeUser, id), map[string]interface{}{
			"entity_name": user.Name,
			"actor_name":  a.Name,
		})
	}

	http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=", id)+url.QueryEscape("Account pending; mail transport accepted the replacement invitation. Previous links are invalid."), http.StatusSeeOther)
}

func (h *UserHandler) ResendRecovery(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	request, ok := authorization.FromContext(r.Context())
	if !ok {
		authorizationError(w, r, authorization.ErrForbidden)
		return
	}
	p, err := request.Service.Administrator(r.Context(), request.Actor)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	u, err := h.svc.GetByID(r.Context(), id)
	if err != nil || u.CompanyID == nil || *u.CompanyID != p.CompanyID {
		http.NotFound(w, r)
		return
	}
	if err := h.emailSvc.CheckAvailability(r.Context()); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=", id)+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	token, err := h.resetSvc.CreateToken(r.Context(), id)
	if err != nil {
		authorizationError(w, r, err)
		return
	}
	link := absoluteAppURL(h.cfg, r, "/reset-password?token="+url.QueryEscape(token))
	if err := h.emailSvc.SendPasswordReset(r.Context(), u.Email, u.Name, link); err != nil {
		slog.Error("send Administrator recovery email", "user_id", id, "error", err)
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=Recovery+email+failed", id), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=Recovery+email+sent", id), http.StatusSeeOther)
}

func normalizedFontSize(fontSize string) string {
	if services.ValidFontSize(fontSize) {
		return fontSize
	}
	return "medium"
}

func (h *UserHandler) invitationMailAvailable(r *http.Request) error {
	if h.emailSvc == nil {
		return fmt.Errorf("email unavailable")
	}
	if err := h.emailSvc.CheckAvailability(r.Context()); err != nil {
		return err
	}
	return h.emailSvc.ValidateConfiguration(r.Context())
}

func userToRow(u *ent.User) templates.UserRow {
	return templates.UserRow{
		ID: u.ID, Name: u.Name, Email: u.Email,
		Role: u.Role, IsActive: u.IsActive,
	}
}
