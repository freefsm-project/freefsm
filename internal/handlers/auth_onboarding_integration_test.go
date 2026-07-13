package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"golang.org/x/crypto/bcrypt"
)

func TestLoginCompletesOnboardingBeforeIssuingCookie(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()

	hash, err := bcrypt.GenerateFromPassword([]byte("Password1!"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := client.User.Create().
		SetCompanyID(77).
		SetEmail("login-onboarding@example.test").
		SetPasswordHash(string(hash)).
		SetName("Login User").
		SetRole("admin").
		SetIsActive(true).
		SaveX(ctx)

	sessions := services.NewSessionService(pool)
	userSvc := services.NewUserService(client)
	activitySvc := services.NewActivityService(client, objectref.NewEntDirectory(client))
	h := NewAuthHandler(pool, sessions, userSvc, nil, nil, nil, nil, activitySvc, nil)
	form := url.Values{"email": {u.Email}, "password": {"Password1!"}}
	r := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	h.login(w, r)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("login status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	completed := client.User.GetX(ctx, u.ID)
	if completed.OnboardingCompletedAt == nil {
		t.Fatal("successful login did not complete onboarding")
	}
	foundSessionCookie := false
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "session" && cookie.Value != "" && cookie.MaxAge != -1 {
			foundSessionCookie = true
			if !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
				t.Fatal("session cookie security attributes changed")
			}
		}
	}
	if !foundSessionCookie {
		t.Fatal("successful login did not issue session cookie")
	}
}

func TestAcceptInviteUsesInvitationCompanyPasswordPolicy(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	const companyID int64 = 78

	client.CompanySettings.Create().
		SetCompanyID(companyID - 1).
		SetBusinessName("Other").
		SetPasswordMinLength(1).
		SetPasswordRequireUppercase(false).
		SetPasswordRequireLowercase(false).
		SetPasswordRequireDigit(false).
		SetPasswordRequireSpecial(false).
		SaveX(ctx)
	client.CompanySettings.Create().
		SetCompanyID(companyID).
		SetBusinessName("Invitation Company").
		SetPasswordMinLength(16).
		SetPasswordRequireUppercase(true).
		SetPasswordRequireLowercase(true).
		SetPasswordRequireDigit(true).
		SetPasswordRequireSpecial(true).
		SaveX(ctx)
	userSvc := services.NewUserService(client)
	inviteSvc := services.NewInvitationService(client)
	u, err := userSvc.Create(ctx, services.UserCreateParams{
		CompanyID:        companyID,
		Name:             "Invited Policy User",
		Email:            "invite-policy@example.test",
		Role:             "tech",
		SendWelcomeEmail: true,
	})
	if err != nil {
		t.Fatalf("create invited user: %v", err)
	}
	token, err := inviteSvc.CreateInvite(ctx, companyID, u.ID)
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	h := NewAuthHandler(nil, nil, userSvc, services.NewCompanySettingsService(client), nil, nil, inviteSvc, nil, nil)

	weak := url.Values{"token": {token}, "password": {"Weak1!"}}
	weakRequest := httptest.NewRequest(http.MethodPost, "/accept-invite", strings.NewReader(weak.Encode()))
	weakRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	weakResponse := httptest.NewRecorder()
	h.AcceptInvite(weakResponse, weakRequest)
	if weakResponse.Code != http.StatusOK {
		t.Fatalf("weak password status = %d, want %d", weakResponse.Code, http.StatusOK)
	}
	if client.User.GetX(ctx, u.ID).IsActive {
		t.Fatal("weak password activated invited user")
	}

	strong := url.Values{"token": {token}, "password": {"StrongPassword1!"}}
	strongRequest := httptest.NewRequest(http.MethodPost, "/accept-invite", strings.NewReader(strong.Encode()))
	strongRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	strongResponse := httptest.NewRecorder()
	h.AcceptInvite(strongResponse, strongRequest)
	if strongResponse.Code != http.StatusSeeOther {
		t.Fatalf("strong password status = %d, want %d", strongResponse.Code, http.StatusSeeOther)
	}
	if !client.User.GetX(ctx, u.ID).IsActive {
		t.Fatal("policy-compliant password did not activate invited user")
	}
}
