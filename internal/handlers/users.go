package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type UserHandler struct {
	svc         *services.UserService
	emailSvc    *services.EmailService
	inviteSvc   *services.InvitationService
	csSvc       *services.CompanySettingsService
	activitySvc *services.ActivityService
	cfg         *config.Config
}

func NewUserHandler(svc *services.UserService, emailSvc *services.EmailService, inviteSvc *services.InvitationService, csSvc *services.CompanySettingsService, activitySvc *services.ActivityService, cfg *config.Config) *UserHandler {
	return &UserHandler{svc: svc, emailSvc: emailSvc, inviteSvc: inviteSvc, csSvc: csSvc, activitySvc: activitySvc, cfg: cfg}
}

func (h *UserHandler) List(w http.ResponseWriter, r *http.Request) {
	users, _ := h.svc.ListAll(r.Context())
	rows := make([]templates.UserRow, len(users))
	for i, u := range users {
		rows[i] = templates.UserRow{
			ID: u.ID, Name: u.Name, Email: u.Email,
			Role: u.Role, IsActive: u.IsActive,
		}
	}
	templates.UsersIndex(templates.UserListData{Users: rows}).Render(r.Context(), w)
}

func (h *UserHandler) Create(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		templates.UserForm(templates.UserFormData{
			IsNew: true,
			User:  &templates.UserDetail{User: templates.UserRow{Role: "tech"}},
			Roles: []string{"admin", "dispatcher", "tech"},
		}).Render(r.Context(), w)
		return
	}
	r.ParseForm()
	result, err := h.svc.Create(r.Context(), services.UserCreateParams{
		Name:             r.FormValue("name"),
		Email:            r.FormValue("email"),
		Password:         r.FormValue("password"),
		Role:             r.FormValue("role"),
		SendWelcomeEmail: r.FormValue("send_welcome_email") == "on",
	})
	if err != nil {
		internalServerError(w, r, "create user", err)
		return
	}

	a, _ := middleware.UserFromContext(r.Context())
	if a != nil && h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), a.ID, "user_created", "user", result.ID, map[string]interface{}{
			"entity_name": result.Name,
			"actor_name":  a.Name,
		})
	}

	if r.FormValue("send_welcome_email") == "on" {
		token, err := h.inviteSvc.CreateInvite(r.Context(), result.ID)
		if err != nil {
			internalServerError(w, r, "create invitation", err)
			return
		}
		inviteURL := absoluteAppURL(h.cfg, r, "/accept-invite?token="+url.QueryEscape(token))
		if err := h.emailSvc.SendWelcomeEmail(r.Context(), result.Email, result.Name, inviteURL); err != nil {
			slog.Error("send welcome email", "error", err, "user", result.Email)
		}
		if a != nil && h.activitySvc != nil {
			h.activitySvc.Record(r.Context(), a.ID, "welcome_invite_sent", "user", result.ID, map[string]interface{}{
				"entity_name": result.Name,
				"actor_name":  a.Name,
			})
		}
	}

	http.Redirect(w, r, "/users?flash=User+created", http.StatusSeeOther)
}

func (h *UserHandler) Show(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	u, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	templates.UserShow(templates.UserDetailPage{User: userToRow(u)}).Render(r.Context(), w)
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
		h.activitySvc.Record(r.Context(), a.ID, "user_updated", "user", id, map[string]interface{}{
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
	h.svc.SetActive(r.Context(), id, newState)

	a, _ := middleware.UserFromContext(r.Context())
	if a != nil && h.activitySvc != nil {
		action := "user_disabled"
		if newState {
			action = "user_enabled"
		}
		h.activitySvc.Record(r.Context(), a.ID, action, "user", id, map[string]interface{}{
			"entity_name": user.Name,
			"actor_name":  a.Name,
		})
	}

	http.Redirect(w, r, "/users?flash=User+toggled", http.StatusSeeOther)
}

func (h *UserHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	r.ParseForm()
	password := r.FormValue("password")
	cs, _ := h.csSvc.Get(r.Context())
	if err := h.svc.ValidatePassword(password, cs); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=%s", id, url.QueryEscape(err.Error())), http.StatusSeeOther)
		return
	}
	if err := h.svc.SetPassword(r.Context(), id, password); err != nil {
		internalServerError(w, r, "admin reset password", err)
		return
	}

	user, err := h.svc.GetByID(r.Context(), id)
	if err == nil {
		a, _ := middleware.UserFromContext(r.Context())
		if a != nil && h.activitySvc != nil {
			h.activitySvc.Record(r.Context(), a.ID, "password_reset", "user", id, map[string]interface{}{
				"entity_name": user.Name,
				"actor_name":  a.Name,
			})
		}
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
	user, err := h.svc.ResendWelcomeEmail(r.Context(), id)
	if err != nil {
		internalServerError(w, r, "prepare welcome resend", err)
		return
	}
	token, err := h.inviteSvc.CreateInvite(r.Context(), user.ID)
	if err != nil {
		internalServerError(w, r, "create invitation", err)
		return
	}

	a, _ := middleware.UserFromContext(r.Context())
	if a != nil && h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), a.ID, "welcome_resent", "user", id, map[string]interface{}{
			"entity_name": user.Name,
			"actor_name":  a.Name,
		})
	}

	inviteURL := absoluteAppURL(h.cfg, r, "/accept-invite?token="+url.QueryEscape(token))
	if err := h.emailSvc.SendWelcomeEmail(r.Context(), user.Email, user.Name, inviteURL); err != nil {
		slog.Error("resend welcome email", "error", err, "user", user.Email)
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=Welcome+email+failed", id), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=Welcome+email+resent", id), http.StatusSeeOther)
}

func normalizedFontSize(fontSize string) string {
	if services.ValidFontSize(fontSize) {
		return fontSize
	}
	return "medium"
}

func userToRow(u *ent.User) templates.UserRow {
	return templates.UserRow{
		ID: u.ID, Name: u.Name, Email: u.Email,
		Role: u.Role, IsActive: u.IsActive,
	}
}
