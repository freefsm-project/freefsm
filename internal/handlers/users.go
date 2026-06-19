package handlers

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/MartialM1nd/freefsm/internal/ent"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

type UserHandler struct {
	svc     *services.UserService
	emailSvc *services.EmailService
	csSvc   *services.CompanySettingsService
}

func NewUserHandler(svc *services.UserService, emailSvc *services.EmailService, csSvc *services.CompanySettingsService) *UserHandler {
	return &UserHandler{svc: svc, emailSvc: emailSvc, csSvc: csSvc}
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
	u, tempPass, err := h.svc.Create(r.Context(), services.UserCreateParams{
		Name:             r.FormValue("name"),
		Email:            r.FormValue("email"),
		Password:         r.FormValue("password"),
		Role:             r.FormValue("role"),
		SendWelcomeEmail: r.FormValue("send_welcome_email") == "on",
	})
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	if r.FormValue("send_welcome_email") == "on" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		loginURL := fmt.Sprintf("%s://%s/login", scheme, r.Host)
		if err := h.emailSvc.SendWelcomeEmail(r.Context(), u.Email, u.Name, tempPass, loginURL); err != nil {
			// Log but don't fail — user was created
			slog.Error("send welcome email", "error", err, "user", u.Email)
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
		http.Error(w, err.Error(), 500)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=User+updated", id), http.StatusSeeOther)
}

func (h *UserHandler) Disable(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	u, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h.svc.SetActive(r.Context(), id, !u.IsActive)
	http.Redirect(w, r, "/users?flash=User+toggled", http.StatusSeeOther)
}

func (h *UserHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	r.ParseForm()
	h.svc.SetPassword(r.Context(), id, r.FormValue("password"))
	http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=Password+reset", id), http.StatusSeeOther)
}

func (h *UserHandler) ResendWelcome(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	u, tempPass, err := h.svc.ResendWelcomeEmail(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	loginURL := fmt.Sprintf("%s://%s/login", scheme, r.Host)
	if err := h.emailSvc.SendWelcomeEmail(r.Context(), u.Email, u.Name, tempPass, loginURL); err != nil {
		slog.Error("resend welcome email", "error", err, "user", u.Email)
		http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=Welcome+email+failed", id), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("/users/%d?flash=Welcome+email+resent", id), http.StatusSeeOther)
}

func userToRow(u *ent.User) templates.UserRow {
	return templates.UserRow{
		ID: u.ID, Name: u.Name, Email: u.Email,
		Role: u.Role, IsActive: u.IsActive,
	}
}
