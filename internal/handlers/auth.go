package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type AuthHandler struct {
	db          *pgxpool.Pool
	sessions    *services.SessionService
	userSvc     *services.UserService
	csSvc       *services.CompanySettingsService
	emailSvc    *services.EmailService
	resetSvc    *services.PasswordResetService
	inviteSvc   *services.InvitationService
	activitySvc *services.ActivityService
	cfg         *config.Config
}

func NewAuthHandler(db *pgxpool.Pool, sessions *services.SessionService, userSvc *services.UserService, csSvc *services.CompanySettingsService, emailSvc *services.EmailService, resetSvc *services.PasswordResetService, inviteSvc *services.InvitationService, activitySvc *services.ActivityService, cfg *config.Config) *AuthHandler {
	return &AuthHandler{db: db, sessions: sessions, userSvc: userSvc, csSvc: csSvc, emailSvc: emailSvc, resetSvc: resetSvc, inviteSvc: inviteSvc, activitySvc: activitySvc, cfg: cfg}
}

func (h *AuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.showLogin(w, r)
	case http.MethodPost:
		h.login(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (h *AuthHandler) showLogin(w http.ResponseWriter, r *http.Request) {
	if needsSetup(r.Context(), h.db) {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	render(w, r, templates.LoginPage(templates.LoginPageData{
		Error: r.URL.Query().Get("error"),
	}))
}

func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=invalid+form", http.StatusSeeOther)
		return
	}

	email := r.FormValue("email")
	password := r.FormValue("password")

	var id int64
	var companyID int64
	var name string
	var hash string
	var forceChange bool
	err := h.db.QueryRow(r.Context(),
		"SELECT id, company_id, name, password_hash, force_password_change FROM users WHERE email = $1 AND is_active = true", email,
	).Scan(&id, &companyID, &name, &hash, &forceChange)
	if err != nil {
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusSeeOther)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusSeeOther)
		return
	}

	token, err := h.sessions.CreateAuthenticated(r.Context(), id, password, "web", "")
	if errors.Is(err, services.ErrInvalidCredentials) {
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusSeeOther)
		return
	}
	if err != nil {
		internalServerError(w, r, "create session", err)
		return
	}
	if err := h.userSvc.CompleteOnboarding(r.Context(), id); err != nil {
		_ = h.sessions.Delete(context.WithoutCancel(r.Context()), token)
		internalServerError(w, r, "complete onboarding", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: "session", Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: middleware.IsHTTPS(r),
		MaxAge: 604800,
	})

	h.activitySvc.Record(r.Context(), companyID, id, "logged_in", objectref.New(objectref.TypeUser, id), map[string]interface{}{
		"entity_name": name,
		"actor_name":  name,
	})

	if forceChange {
		http.Redirect(w, r, "/change-password", http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *AuthHandler) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		render(w, r, templates.ForgotPasswordPage(templates.ForgotPasswordData{}))
		return
	}
	if err := h.emailSvc.CheckAvailability(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err := h.emailSvc.ValidateConfiguration(r.Context()); err != nil {
		http.Error(w, "Recovery email is unavailable. Contact an Administrator for a temporary password.", http.StatusServiceUnavailable)
		return
	}
	r.ParseForm()
	email := r.FormValue("email")
	if err := h.resetSvc.Request(r.Context(), email, absoluteAppURL(h.cfg, r, "/reset-password")); err != nil {
		http.Error(w, "Recovery requests are temporarily unavailable. Contact an Administrator.", http.StatusServiceUnavailable)
		return
	}
	render(w, r, templates.ForgotPasswordPage(templates.ForgotPasswordData{
		Success: true,
	}))
}

func (h *AuthHandler) ResetPassword(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		token := r.URL.Query().Get("token")
		_, err := h.resetSvc.Validate(r.Context(), token)
		if err != nil {
			render(w, r, templates.ResetPasswordPage(templates.ResetPasswordData{Error: "Invalid or expired reset link"}))
			return
		}
		h.renderRecovery(w, r, token, "")
		return
	}
	r.ParseForm()
	token := r.FormValue("token")
	password := r.FormValue("password")
	uid, err := h.resetSvc.Validate(r.Context(), token)
	if err != nil {
		http.Error(w, "invalid token", 400)
		return
	}
	u, err := h.userSvc.GetByID(r.Context(), uid)
	if err != nil || u.CompanyID == nil {
		http.Error(w, "invalid token", http.StatusBadRequest)
		return
	}
	if err := validatePasswordConfirmation(password, r.FormValue("confirm_password")); err != nil {
		h.renderRecovery(w, r, token, err.Error())
		return
	}
	cs, err := h.csSvc.GetForCompany(r.Context(), *u.CompanyID)
	if err != nil {
		internalServerError(w, r, "get reset password policy", err)
		return
	}
	if err := h.userSvc.ValidatePassword(password, cs); err != nil {
		h.renderRecovery(w, r, token, err.Error())
		return
	}
	if err := h.resetSvc.Redeem(r.Context(), token, password); err != nil {
		h.renderRecovery(w, r, token, "Unable to reset password. Check the current policy or request a new link.")
		return
	}
	http.Redirect(w, r, "/login?flash=Password+reset+successfully", http.StatusSeeOther)
}

func (h *AuthHandler) renderRecovery(w http.ResponseWriter, r *http.Request, token, message string) {
	id, err := h.resetSvc.Validate(r.Context(), token)
	if err != nil {
		http.Error(w, "invalid or expired reset link", http.StatusBadRequest)
		return
	}
	u, err := h.userSvc.GetByID(r.Context(), id)
	if err != nil || u.CompanyID == nil {
		http.Error(w, "invalid or expired reset link", http.StatusBadRequest)
		return
	}
	policy, err := h.csSvc.GetForCompany(r.Context(), *u.CompanyID)
	if err != nil {
		internalServerError(w, r, "get recovery policy", err)
		return
	}
	render(w, r, templates.ResetPasswordPage(templates.ResetPasswordData{Token: token, Valid: true, Error: message, Policy: policy}))
}

func (h *AuthHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		token := r.URL.Query().Get("token")
		target, err := h.inviteSvc.ValidateInvite(r.Context(), token)
		if err != nil {
			render(w, r, templates.AcceptInvitePage(templates.ResetPasswordData{Error: "Invalid or expired invitation"}))
			return
		}
		policy, err := h.csSvc.GetForCompany(r.Context(), target.CompanyID)
		if err != nil {
			internalServerError(w, r, "get invitation policy", err)
			return
		}
		render(w, r, templates.AcceptInvitePage(templates.ResetPasswordData{Token: token, Valid: true, Policy: policy}))
		return
	}

	r.ParseForm()
	token := r.FormValue("token")
	password := r.FormValue("password")
	target, err := h.inviteSvc.ValidateInvite(r.Context(), token)
	if err != nil {
		http.Error(w, "invalid invitation", http.StatusBadRequest)
		return
	}
	cs, err := h.csSvc.GetForCompany(r.Context(), target.CompanyID)
	if err != nil {
		internalServerError(w, r, "get invitation company password policy", err)
		return
	}
	if err := validatePasswordConfirmation(password, r.FormValue("confirm_password")); err != nil {
		render(w, r, templates.AcceptInvitePage(templates.ResetPasswordData{Token: token, Valid: true, Error: err.Error(), Policy: cs}))
		return
	}
	if err := h.userSvc.ValidatePassword(password, cs); err != nil {
		render(w, r, templates.AcceptInvitePage(templates.ResetPasswordData{Token: token, Valid: true, Error: err.Error(), Policy: cs}))
		return
	}
	u, err := h.inviteSvc.AcceptInvite(r.Context(), token, password)
	if errors.Is(err, services.ErrInvitationPassword) {
		policy, loadErr := h.csSvc.GetForCompany(r.Context(), target.CompanyID)
		if loadErr != nil {
			internalServerError(w, r, "reload invitation password policy", loadErr)
			return
		}
		render(w, r, templates.AcceptInvitePage(templates.ResetPasswordData{Token: token, Valid: true, Error: err.Error(), Policy: policy}))
		return
	}
	if errors.Is(err, services.ErrInvalidInvitation) {
		http.Error(w, "invalid invitation", http.StatusBadRequest)
		return
	}
	if err != nil {
		internalServerError(w, r, "accept invitation", err)
		return
	}
	if h.activitySvc != nil {
		h.activitySvc.Record(r.Context(), target.CompanyID, u.ID, "invite_accepted", objectref.New(objectref.TypeUser, u.ID), map[string]interface{}{
			"entity_name": u.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/login?flash=Account+activated", http.StatusSeeOther)
}
