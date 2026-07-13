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

	request := func(email, password string) *httptest.ResponseRecorder {
		form := url.Values{
			"name":     {"Direct User"},
			"email":    {email},
			"password": {password},
			"role":     {"tech"},
		}
		r := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), middleware.UserKey, &middleware.UserInfo{ID: 100, CompanyID: companyID, Name: "Admin", Role: "admin"}))
		w := httptest.NewRecorder()
		h.Create(w, r)
		return w
	}

	weakResponse := request("weak-direct@example.test", "Weak1!")
	if weakResponse.Code != http.StatusSeeOther || !strings.HasPrefix(weakResponse.Header().Get("Location"), "/users/new?flash=") {
		t.Fatalf("weak password response = (%d, %q)", weakResponse.Code, weakResponse.Header().Get("Location"))
	}
	if got := client.User.Query().CountX(ctx); got != 0 {
		t.Fatalf("weak password created %d users", got)
	}

	strongResponse := request("strong-direct@example.test", "StrongPassword1!")
	if strongResponse.Code != http.StatusSeeOther || strongResponse.Header().Get("Location") != "/users?flash=User+created" {
		t.Fatalf("strong password response = (%d, %q)", strongResponse.Code, strongResponse.Header().Get("Location"))
	}
	created := client.User.Query().OnlyX(ctx)
	if created.CompanyID == nil || *created.CompanyID != companyID || !created.IsActive || created.OnboardingCompletedAt == nil {
		t.Fatal("valid direct user was not created as company-owned and completed")
	}
}
