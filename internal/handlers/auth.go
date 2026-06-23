package handlers

import (
	"fmt"
	"net/http"

	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
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
	activitySvc *services.ActivityService
}

func NewAuthHandler(db *pgxpool.Pool, sessions *services.SessionService, userSvc *services.UserService, csSvc *services.CompanySettingsService, emailSvc *services.EmailService, resetSvc *services.PasswordResetService, activitySvc *services.ActivityService) *AuthHandler {
	return &AuthHandler{db: db, sessions: sessions, userSvc: userSvc, csSvc: csSvc, emailSvc: emailSvc, resetSvc: resetSvc, activitySvc: activitySvc}
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
		http.Error(w, err.Error(), 500)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: "session", Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: middleware.IsHTTPS(r),
		MaxAge: 604800,
	})

	h.activitySvc.Record(r.Context(), id, "logged_in", "user", id, map[string]interface{}{
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
		http.Error(w, err.Error(), 500)
		return
	}
	scheme := "http"
	if middleware.IsHTTPS(r) {
		scheme = "https"
	}
	link := fmt.Sprintf("%s://%s/reset-password?token=%s", scheme, r.Host, tok)
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
