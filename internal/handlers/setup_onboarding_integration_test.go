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

	h := NewSetupHandler(pool, services.NewSessionService(pool), services.NewUserService(client), services.NewCompanySettingsService(client), &config.Config{SetupToken: "setup-secret"})
	form := url.Values{
		"token":            {"setup-secret"},
		"name":             {"Setup Admin"},
		"email":            {"setup-admin@example.test"},
		"password":         {"Password1!"},
		"confirm_password": {"Password1!"},
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

	h := NewSetupHandler(pool, services.NewSessionService(pool), services.NewUserService(client), services.NewCompanySettingsService(client), &config.Config{SetupToken: "setup-secret"})
	form := url.Values{
		"token":            {"setup-secret"},
		"name":             {"Setup Admin"},
		"email":            {"ambiguous-admin@example.test"},
		"password":         {"Password1!"},
		"confirm_password": {"Password1!"},
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

func TestSetupAdminRequiresConfirmationAndSoleCompanyPolicy(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	var companyID int64
	if err := pool.QueryRow(ctx, `INSERT INTO companies(name, slug) VALUES('Strict Setup', 'strict-setup') RETURNING id`).Scan(&companyID); err != nil {
		t.Fatalf("create setup company: %v", err)
	}
	client.CompanySettings.Create().
		SetCompanyID(companyID).
		SetBusinessName("Strict Setup").
		SetPasswordMinLength(16).
		SetPasswordRequireUppercase(true).
		SetPasswordRequireLowercase(true).
		SetPasswordRequireDigit(true).
		SetPasswordRequireSpecial(true).
		SaveX(ctx)
	h := NewSetupHandler(pool, services.NewSessionService(pool), services.NewUserService(client), services.NewCompanySettingsService(client), &config.Config{SetupToken: "setup-secret"})

	request := func(password, confirmation string) *httptest.ResponseRecorder {
		form := url.Values{
			"token":            {"setup-secret"},
			"name":             {"Setup Admin"},
			"email":            {"strict-setup@example.test"},
			"password":         {password},
			"confirm_password": {confirmation},
		}
		r := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	for name, confirmation := range map[string]string{"missing": "", "mismatch": "DifferentPassword1!"} {
		t.Run(name+" confirmation", func(t *testing.T) {
			response := request("StrongSetupPassword1!", confirmation)
			if response.Code != http.StatusSeeOther || client.User.Query().CountX(ctx) != 0 {
				t.Fatalf("confirmation failure response = %d, users = %d", response.Code, client.User.Query().CountX(ctx))
			}
		})
	}
	weak := request("Weak1!", "Weak1!")
	if weak.Code != http.StatusSeeOther || client.User.Query().CountX(ctx) != 0 {
		t.Fatalf("weak setup response = %d, users = %d", weak.Code, client.User.Query().CountX(ctx))
	}
	strong := request("StrongSetupPassword1!", "StrongSetupPassword1!")
	if strong.Code != http.StatusSeeOther || strong.Header().Get("Location") != "/setup/company" {
		t.Fatalf("strong setup response = (%d, %q)", strong.Code, strong.Header().Get("Location"))
	}
}
