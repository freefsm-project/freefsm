package handlers

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/passwordpolicy"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type SetupHandler struct {
	db       *pgxpool.Pool
	sessions *services.SessionService
	userSvc  *services.UserService
	csSvc    *services.CompanySettingsService
	cfg      *config.Config
}

func NewSetupHandler(db *pgxpool.Pool, sessions *services.SessionService, userSvc *services.UserService, csSvc *services.CompanySettingsService, cfg *config.Config) *SetupHandler {
	return &SetupHandler{db: db, sessions: sessions, userSvc: userSvc, csSvc: csSvc, cfg: cfg}
}

func (h *SetupHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !needsSetup(r.Context(), h.db) {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.show(w, r)
	case http.MethodPost:
		h.create(w, r)
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (h *SetupHandler) show(w http.ResponseWriter, r *http.Request) {
	render(w, r, templates.SetupPage(templates.SetupPageData{
		Error: r.URL.Query().Get("error"),
	}))
}

func (h *SetupHandler) create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/setup?error=invalid+form", http.StatusSeeOther)
		return
	}

	if subtle.ConstantTimeCompare([]byte(r.FormValue("token")), []byte(h.cfg.SetupToken)) != 1 {
		http.Redirect(w, r, "/setup?error=invalid+setup+token", http.StatusSeeOther)
		return
	}

	name := r.FormValue("name")
	email := r.FormValue("email")
	password := r.FormValue("password")

	if name == "" || email == "" {
		http.Redirect(w, r, "/setup?error=all+fields+required", http.StatusSeeOther)
		return
	}
	if err := validatePasswordConfirmation(password, r.FormValue("confirm_password")); err != nil {
		http.Redirect(w, r, "/setup?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	var companyID int64
	if err := h.db.QueryRow(r.Context(), `SELECT min(id) FROM companies HAVING count(*) = 1`).Scan(&companyID); err != nil {
		internalServerError(w, r, "resolve setup company", fmt.Errorf("setup requires exactly one company: %w", err))
		return
	}
	cs, err := h.csSvc.GetForCompany(r.Context(), companyID)
	if err != nil && !ent.IsNotFound(err) {
		internalServerError(w, r, "get setup password policy", err)
		return
	}
	if err := h.userSvc.ValidatePassword(password, cs); err != nil {
		http.Redirect(w, r, "/setup?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		internalServerError(w, r, "hash setup password", err)
		return
	}

	tx, err := h.db.Begin(r.Context())
	if err != nil {
		internalServerError(w, r, "begin setup", err)
		return
	}
	defer tx.Rollback(context.WithoutCancel(r.Context()))
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(190019); LOCK TABLE users IN EXCLUSIVE MODE`); err != nil {
		internalServerError(w, r, "lock setup", err)
		return
	}
	var initialized bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM users)`).Scan(&initialized); err != nil {
		internalServerError(w, r, "check setup", err)
		return
	}
	if initialized {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	policy, err := passwordpolicy.Load(r.Context(), tx, companyID)
	if err != nil {
		internalServerError(w, r, "lock setup policy", err)
		return
	}
	if err = policy.Validate(password); err != nil {
		http.Redirect(w, r, "/setup?error="+url.QueryEscape(err.Error()), http.StatusSeeOther)
		return
	}
	var userID int64
	err = tx.QueryRow(r.Context(),
		`INSERT INTO users (company_id, email, password_hash, name, role, onboarding_completed_at) VALUES ($1, $2, $3, $4, 'admin', NOW()) RETURNING id`,
		companyID, email, string(hash), name,
	).Scan(&userID)
	if err != nil {
		if isUniqueViolation(err) {
			http.Redirect(w, r, "/setup?error=email+already+in+use", http.StatusSeeOther)
			return
		}
		internalServerError(w, r, "create setup admin", err)
		return
	}
	var roleID int64
	if err = tx.QueryRow(r.Context(), `SELECT authorization_role_id FROM users WHERE id=$1`, userID).Scan(&roleID); err != nil {
		internalServerError(w, r, "resolve setup Role", err)
		return
	}
	if err = protectedaudit.RecordProduct(r.Context(), tx, protectedaudit.ProductAction{ActorID: userID, RoleID: roleID, Action: "user.create", Scope: "S", TargetType: "user", TargetID: userID, Channel: "setup"}); err != nil {
		internalServerError(w, r, "audit setup", err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		internalServerError(w, r, "commit setup", err)
		return
	}

	// Account creation has committed. Revalidate the submitted credential under
	// the session issuer's mutex so an intervening reset cannot publish a stale
	// setup session after revocation.
	token, err := h.sessions.CreateAuthenticated(r.Context(), userID, password, "web", "")
	if errors.Is(err, services.ErrInvalidCredentials) {
		http.Redirect(w, r, "/login?error=credentials+changed%3B+sign+in+again", http.StatusSeeOther)
		return
	}
	if err != nil {
		internalServerError(w, r, "create setup session", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: "session", Value: token, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: middleware.IsHTTPS(r),
		MaxAge: 604800,
	})

	http.Redirect(w, r, "/setup/company", http.StatusSeeOther)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func needsSetup(ctx context.Context, db *pgxpool.Pool) bool {
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		slog.Error("needsSetup: query failed", "error", err)
		return false
	}
	return count == 0
}
