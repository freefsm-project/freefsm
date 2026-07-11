package handlers

import (
	"net/http"
	"net/url"

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
	var name string
	var hash string
	var forceChange bool
	err := h.db.QueryRow(r.Context(),
		"SELECT id, name, password_hash, force_password_change FROM users WHERE email = $1 AND is_active = true", email,
	).Scan(&id, &name, &hash, &forceChange)
	if err != nil {
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusSeeOther)
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		http.Redirect(w, r, "/login?error=invalid+credentials", http.StatusSeeOther)
		return
	}

	token, err := h.sessions.Create(r.Context(), id)
	if err != nil {
		internalServerError(w, r, "create session", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: "session", Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: middleware.IsHTTPS(r),
		MaxAge: 604800,
	})

	h.activitySvc.Record(r.Context(), id, "logged_in", objectref.New(objectref.TypeUser, id), map[string]interface{}{
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
	r.ParseForm()
	email := r.FormValue("email")
	u, err := h.userSvc.GetByEmail(r.Context(), email)
	if err != nil {
		render(w, r, templates.ForgotPasswordPage(templates.ForgotPasswordData{Success: true}))
		return
	}
	tok, err := h.resetSvc.CreateToken(r.Context(), u.ID)
	if err != nil {
		internalServerError(w, r, "create password reset token", err)
		return
	}
	link := absoluteAppURL(h.cfg, r, "/reset-password?token="+url.QueryEscape(tok))
	var emailErr string
	if err := h.emailSvc.SendPasswordReset(r.Context(), email, u.Name, link); err != nil {
		emailErr = "Failed to send email. Please try again."
	}
	render(w, r, templates.ForgotPasswordPage(templates.ForgotPasswordData{
		Success:  true,
		EMailErr: emailErr,
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
		render(w, r, templates.ResetPasswordPage(templates.ResetPasswordData{Token: token, Valid: true}))
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
	cs, _ := h.csSvc.Get(r.Context())
	if err := h.userSvc.ValidatePassword(password, cs); err != nil {
		render(w, r, templates.ResetPasswordPage(templates.ResetPasswordData{
			Token: token, Valid: true, Error: err.Error(),
		}))
		return
	}
	if err := h.userSvc.SetPassword(r.Context(), uid, password); err != nil {
		render(w, r, templates.ResetPasswordPage(templates.ResetPasswordData{
			Token: token, Valid: true, Error: "Failed to reset password. Please try again.",
		}))
		return
	}
	h.resetSvc.Consume(r.Context(), token)
	http.Redirect(w, r, "/login?flash=Password+reset+successfully", http.StatusSeeOther)
}

func (h *AuthHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		token := r.URL.Query().Get("token")
		_, err := h.inviteSvc.ValidateInvite(r.Context(), token)
		if err != nil {
			render(w, r, templates.AcceptInvitePage(templates.ResetPasswordData{Error: "Invalid or expired invitation"}))
			return
		}
		render(w, r, templates.AcceptInvitePage(templates.ResetPasswordData{Token: token, Valid: true}))
		return
	}

	r.ParseForm()
	token := r.FormValue("token")
	password := r.FormValue("password")
	uid, err := h.inviteSvc.ValidateInvite(r.Context(), token)
	if err != nil {
		http.Error(w, "invalid invitation", http.StatusBadRequest)
		return
	}
	cs, _ := h.csSvc.Get(r.Context())
	if err := h.userSvc.ValidatePassword(password, cs); err != nil {
		render(w, r, templates.AcceptInvitePage(templates.ResetPasswordData{Token: token, Valid: true, Error: err.Error()}))
		return
	}
	if err := h.userSvc.ActivateWithPassword(r.Context(), uid, password); err != nil {
		internalServerError(w, r, "activate invited user", err)
		return
	}
	if err := h.inviteSvc.ConsumeInvite(r.Context(), token); err != nil {
		internalServerError(w, r, "consume invitation", err)
		return
	}
	if u, err := h.userSvc.GetByID(r.Context(), uid); err == nil {
		h.activitySvc.Record(r.Context(), uid, "invite_accepted", objectref.New(objectref.TypeUser, uid), map[string]interface{}{
			"entity_name": u.Name,
			"actor_name":  u.Name,
		})
	}
	http.Redirect(w, r, "/login?flash=Account+activated", http.StatusSeeOther)
}
