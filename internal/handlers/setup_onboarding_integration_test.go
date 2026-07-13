package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUniqueViolation(t *testing.T) {
	uniqueErr := fmt.Errorf("insert setup admin: %w", &pgconn.PgError{Code: "23505"})
	if !isUniqueViolation(uniqueErr) {
		t.Fatal("wrapped 23505 error was not recognized as a unique violation")
	}
	if isUniqueViolation(&pgconn.PgError{Code: "23503"}) {
		t.Fatal("non-unique PostgreSQL error was classified as a unique violation")
	}
	if isUniqueViolation(fmt.Errorf("plain insert failure")) {
		t.Fatal("plain error was classified as a unique violation")
	}
}

func TestSetupAdminUsesSoleCompanyAndCompletesOnboarding(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	var companyID int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name, slug) VALUES('Setup', 'setup') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("create setup company: %v", err)
	}

	h := NewSetupHandler(pool, services.NewSessionService(pool), &config.Config{SetupToken: "setup-secret"})
	form := url.Values{
		"token":    {"setup-secret"},
		"name":     {"Setup Admin"},
		"email":    {"setup-admin@example.test"},
		"password": {"Password1!"},
	}
	r := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/setup/company" {
		t.Fatalf("setup response = (%d, %q)", w.Code, w.Header().Get("Location"))
	}
	u := client.User.Query().OnlyX(ctx)
	if u.CompanyID == nil || *u.CompanyID != companyID || u.OnboardingCompletedAt == nil {
		t.Fatal("setup admin lacks company ownership or onboarding completion")
	}
}

func TestSetupAdminRejectsAmbiguousCompanyOwnership(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO companies(name, slug) VALUES('One', 'one'), ('Two', 'two')`); err != nil {
		t.Fatalf("create setup companies: %v", err)
	}

	h := NewSetupHandler(pool, services.NewSessionService(pool), &config.Config{SetupToken: "setup-secret"})
	form := url.Values{
		"token":    {"setup-secret"},
		"name":     {"Setup Admin"},
		"email":    {"ambiguous-admin@example.test"},
		"password": {"Password1!"},
	}
	r := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("ambiguous setup status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
	if got := client.User.Query().CountX(ctx); got != 0 {
		t.Fatalf("ambiguous setup created %d users", got)
	}
}
