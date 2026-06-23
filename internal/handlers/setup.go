package handlers

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/MartialM1nd/freefsm/internal/config"
	"github.com/MartialM1nd/freefsm/internal/middleware"
	"github.com/MartialM1nd/freefsm/internal/services"
	"github.com/MartialM1nd/freefsm/internal/templates"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

type SetupHandler struct {
	db       *pgxpool.Pool
	sessions *services.SessionService
	cfg      *config.Config
}

func NewSetupHandler(db *pgxpool.Pool, sessions *services.SessionService, cfg *config.Config) *SetupHandler {
	return &SetupHandler{db: db, sessions: sessions, cfg: cfg}
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

	if r.FormValue("token") != h.cfg.SetupToken {
		http.Redirect(w, r, "/setup?error=invalid+setup+token", http.StatusSeeOther)
		return
	}

	name := r.FormValue("name")
	email := r.FormValue("email")
	password := r.FormValue("password")

	if name == "" || email == "" || password == "" {
		http.Redirect(w, r, "/setup?error=all+fields+required", http.StatusSeeOther)
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}

	var userID int64
	err = h.db.QueryRow(r.Context(),
		`INSERT INTO users (email, password_hash, name, role) VALUES ($1, $2, $3, 'admin') RETURNING id`,
		email, string(hash), name,
	).Scan(&userID)
	if err != nil {
		http.Redirect(w, r, "/setup?error=email+already+in+use", http.StatusSeeOther)
		return
	}

	token, err := h.sessions.Create(r.Context(), userID)
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

	http.Redirect(w, r, "/setup/company", http.StatusSeeOther)
}

func needsSetup(ctx context.Context, db *pgxpool.Pool) bool {
	var count int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
		slog.Error("needsSetup: query failed", "error", err)
		return false
	}
	return count == 0
}
