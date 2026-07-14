package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent/invitationtoken"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

func TestResendWelcomeRejectsIneligiblePostWithoutMutation(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	const companyID int64 = 91

	userSvc := services.NewUserService(client)
	inviteSvc := services.NewInvitationService(client)
	u, err := userSvc.Create(ctx, services.UserCreateParams{
		CompanyID:        companyID,
		Name:             "Established User",
		Email:            "established-resend@example.test",
		Role:             "tech",
		SendWelcomeEmail: true,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := inviteSvc.CreateInvite(ctx, companyID, u.ID); err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	client.User.UpdateOneID(u.ID).SetOnboardingCompletedAt(time.Now()).ExecX(ctx)
	beforeUser := client.User.GetX(ctx, u.ID)
	beforeToken := client.InvitationToken.Query().Where(invitationtoken.UserIDEQ(u.ID)).OnlyX(ctx)

	h := NewUserHandler(userSvc, nil, inviteSvc, nil, nil, nil)
	for _, actingCompanyID := range []int64{companyID, companyID + 1} {
		r := httptest.NewRequest(http.MethodPost, "/users/1/resend-welcome", nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", strconv.FormatInt(u.ID, 10))
		rctx.URLParams.Add("action", "resend-welcome")
		requestCtx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
		requestCtx = context.WithValue(requestCtx, middleware.UserKey, &middleware.UserInfo{ID: 100, CompanyID: actingCompanyID, Name: "Admin", Role: "admin"})
		w := httptest.NewRecorder()

		h.ResendWelcome(w, r.WithContext(requestCtx))

		if w.Code != http.StatusConflict {
			t.Fatalf("company %d resend status = %d, want %d", actingCompanyID, w.Code, http.StatusConflict)
		}
	}
	afterUser := client.User.GetX(ctx, u.ID)
	afterTokens := client.InvitationToken.Query().Where(invitationtoken.UserIDEQ(u.ID)).AllX(ctx)
	if afterUser.IsActive != beforeUser.IsActive || !afterUser.WelcomeEmailSentAt.Equal(*beforeUser.WelcomeEmailSentAt) {
		t.Fatal("rejected handler request mutated user")
	}
	if len(afterTokens) != 1 || afterTokens[0].ConsumedAt != nil || beforeToken.ConsumedAt != nil {
		t.Fatal("rejected handler request mutated or created invitation")
	}
}

func TestCreateDirectUserValidatesActingCompanyPasswordPolicy(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	const companyID int64 = 92

	client.CompanySettings.Create().
		SetCompanyID(companyID).
		SetBusinessName("Direct User Company").
		SetPasswordMinLength(14).
		SetPasswordRequireUppercase(true).
		SetPasswordRequireLowercase(true).
		SetPasswordRequireDigit(true).
		SetPasswordRequireSpecial(true).
		SaveX(ctx)
	userSvc := services.NewUserService(client)
	h := NewUserHandler(userSvc, nil, services.NewInvitationService(client), services.NewCompanySettingsService(client), nil, nil)

	request := func(email, password, confirmation string) *httptest.ResponseRecorder {
		form := url.Values{
			"name":             {"Direct User"},
			"email":            {email},
			"password":         {password},
			"confirm_password": {confirmation},
			"role":             {"tech"},
		}
		r := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), middleware.UserKey, &middleware.UserInfo{ID: 100, CompanyID: companyID, Name: "Admin", Role: "admin"}))
		w := httptest.NewRecorder()
		h.Create(w, r)
		return w
	}

	for name, confirmation := range map[string]string{"missing": "", "mismatch": "DifferentPassword1!"} {
		response := request(name+"-direct@example.test", "StrongPassword1!", confirmation)
		if response.Code != http.StatusSeeOther || client.User.Query().CountX(ctx) != 0 {
			t.Fatalf("%s confirmation response = %d, users = %d", name, response.Code, client.User.Query().CountX(ctx))
		}
	}

	weakResponse := request("weak-direct@example.test", "Weak1!", "Weak1!")
	if weakResponse.Code != http.StatusSeeOther || !strings.HasPrefix(weakResponse.Header().Get("Location"), "/users/new?flash=") {
		t.Fatalf("weak password response = (%d, %q)", weakResponse.Code, weakResponse.Header().Get("Location"))
	}
	if got := client.User.Query().CountX(ctx); got != 0 {
		t.Fatalf("weak password created %d users", got)
	}

	strongResponse := request("strong-direct@example.test", "StrongPassword1!", "StrongPassword1!")
	if strongResponse.Code != http.StatusSeeOther || strongResponse.Header().Get("Location") != "/users?flash=User+created" {
		t.Fatalf("strong password response = (%d, %q)", strongResponse.Code, strongResponse.Header().Get("Location"))
	}
	created := client.User.Query().OnlyX(ctx)
	if created.CompanyID == nil || *created.CompanyID != companyID || !created.IsActive || created.OnboardingCompletedAt == nil {
		t.Fatal("valid direct user was not created as company-owned and completed")
	}
}

func TestCreateWelcomeUserDoesNotRequirePasswordConfirmation(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	const companyID int64 = 93

	client.CompanySettings.Create().SetCompanyID(companyID).SetBusinessName("Welcome Company").SaveX(ctx)
	userSvc := services.NewUserService(client)
	settingsSvc := services.NewCompanySettingsService(client)
	h := NewUserHandler(userSvc, services.NewEmailService(settingsSvc), services.NewInvitationService(client), settingsSvc, nil, nil)
	const stalePassword = "StalePassword1!"
	form := url.Values{
		"name":               {"Welcome User"},
		"email":              {"welcome@example.test"},
		"password":           {stalePassword},
		"confirm_password":   {"DifferentStalePassword1!"},
		"role":               {"tech"},
		"send_welcome_email": {"on"},
	}
	r := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r = r.WithContext(context.WithValue(r.Context(), middleware.UserKey, &middleware.UserInfo{ID: 100, CompanyID: companyID, Name: "Admin", Role: "admin"}))
	w := httptest.NewRecorder()
	h.Create(w, r)

	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/users?flash=User+created" {
		t.Fatalf("welcome creation response = (%d, %q)", w.Code, w.Header().Get("Location"))
	}
	created := client.User.Query().OnlyX(ctx)
	if created.IsActive {
		t.Fatal("welcome user should remain inactive pending invitation")
	}
	if bcrypt.CompareHashAndPassword([]byte(created.PasswordHash), []byte(stalePassword)) == nil {
		t.Fatal("welcome user retained the stale submitted password")
	}
}

func TestAdminResetPasswordIsCompanyScopedAndRequiresConfirmation(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	const companyID int64 = 94

	client.CompanySettings.Create().SetCompanyID(companyID).SetBusinessName("Admin Company").SetPasswordMinLength(12).SaveX(ctx)
	client.CompanySettings.Create().SetCompanyID(companyID + 1).SetBusinessName("Foreign Company").SetPasswordMinLength(1).SaveX(ctx)
	oldHash, err := bcrypt.GenerateFromPassword([]byte("OldPassword1!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	target := client.User.Create().SetCompanyID(companyID).SetEmail("target@example.test").SetName("Target").SetRole("tech").SetPasswordHash(string(oldHash)).SaveX(ctx)
	foreign := client.User.Create().SetCompanyID(companyID + 1).SetEmail("foreign@example.test").SetName("Foreign").SetRole("tech").SetPasswordHash(string(oldHash)).SaveX(ctx)
	h := NewUserHandler(services.NewUserService(client), nil, nil, services.NewCompanySettingsService(client), nil, nil)

	request := func(userID int64, confirmation string) *httptest.ResponseRecorder {
		form := url.Values{"password": {"NewPassword1!"}, "confirm_password": {confirmation}}
		r := httptest.NewRequest(http.MethodPost, "/users/1/reset-password", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", strconv.FormatInt(userID, 10))
		requestCtx := context.WithValue(r.Context(), chi.RouteCtxKey, rctx)
		requestCtx = context.WithValue(requestCtx, middleware.UserKey, &middleware.UserInfo{ID: 100, CompanyID: companyID, Name: "Admin", Role: "admin"})
		w := httptest.NewRecorder()
		h.ResetPassword(w, r.WithContext(requestCtx))
		return w
	}

	if response := request(foreign.ID, "NewPassword1!"); response.Code != http.StatusNotFound {
		t.Fatalf("foreign reset status = %d, want %d", response.Code, http.StatusNotFound)
	}
	for name, confirmation := range map[string]string{"missing": "", "mismatch": "DifferentPassword1!"} {
		if response := request(target.ID, confirmation); response.Code != http.StatusSeeOther {
			t.Fatalf("%s confirmation status = %d", name, response.Code)
		}
		if bcrypt.CompareHashAndPassword([]byte(client.User.GetX(ctx, target.ID).PasswordHash), []byte("OldPassword1!")) != nil {
			t.Fatalf("%s confirmation changed target password", name)
		}
	}
	if response := request(target.ID, "NewPassword1!"); response.Code != http.StatusSeeOther {
		t.Fatalf("matching confirmation status = %d", response.Code)
	}
	if bcrypt.CompareHashAndPassword([]byte(client.User.GetX(ctx, target.ID).PasswordHash), []byte("NewPassword1!")) != nil {
		t.Fatal("matching confirmation did not change target password")
	}
	if bcrypt.CompareHashAndPassword([]byte(client.User.GetX(ctx, foreign.ID).PasswordHash), []byte("OldPassword1!")) != nil {
		t.Fatal("foreign reset attempt changed password")
	}
}
