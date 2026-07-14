package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"golang.org/x/crypto/bcrypt"
)

func TestChangePasswordRendersErrorsAndPreservesForceStateUntilSuccess(t *testing.T) {
	client, pool := openHandlerTestDB(t)
	defer client.Close()
	defer pool.Close()
	ctx := context.Background()
	const companyID int64 = 95

	client.CompanySettings.Create().SetCompanyID(companyID - 1).SetBusinessName("Other").SetPasswordMinLength(1).SaveX(ctx)
	client.CompanySettings.Create().
		SetCompanyID(companyID).
		SetBusinessName("Current Company").
		SetPasswordMinLength(14).
		SetPasswordRequireUppercase(true).
		SetPasswordRequireLowercase(true).
		SetPasswordRequireDigit(true).
		SetPasswordRequireSpecial(true).
		SaveX(ctx)
	oldHash, err := bcrypt.GenerateFromPassword([]byte("OldPassword1!"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().
		SetCompanyID(companyID).
		SetEmail("change@example.test").
		SetName("Change User").
		SetRole("admin").
		SetPasswordHash(string(oldHash)).
		SetForcePasswordChange(true).
		SaveX(ctx)
	h := NewPasswordHandler(services.NewUserService(client), services.NewCompanySettingsService(client), nil)
	userContext := &middleware.UserInfo{ID: u.ID, CompanyID: companyID, Name: u.Name, Role: "admin"}

	get := httptest.NewRequest(http.MethodGet, "/change-password?error=visible+error", nil)
	get = get.WithContext(context.WithValue(get.Context(), middleware.UserKey, userContext))
	getResponse := httptest.NewRecorder()
	h.ChangePassword(getResponse, get)
	if getResponse.Code != http.StatusOK || !strings.Contains(getResponse.Body.String(), "visible error") {
		t.Fatalf("GET did not render query error: status=%d body=%q", getResponse.Code, getResponse.Body.String())
	}

	request := func(password, confirmation string) *httptest.ResponseRecorder {
		form := url.Values{
			"current_password": {"OldPassword1!"},
			"new_password":     {password},
			"confirm_password": {confirmation},
		}
		r := httptest.NewRequest(http.MethodPost, "/change-password", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r = r.WithContext(context.WithValue(r.Context(), middleware.UserKey, userContext))
		w := httptest.NewRecorder()
		h.ChangePassword(w, r)
		return w
	}

	for name, confirmation := range map[string]string{"missing": "", "mismatch": "DifferentPassword1!"} {
		response := request("StrongNewPassword1!", confirmation)
		if response.Code != http.StatusSeeOther || !strings.Contains(response.Header().Get("Location"), "error=") {
			t.Fatalf("%s confirmation response = (%d, %q)", name, response.Code, response.Header().Get("Location"))
		}
		unchanged := client.User.GetX(ctx, u.ID)
		if !unchanged.ForcePasswordChange || bcrypt.CompareHashAndPassword([]byte(unchanged.PasswordHash), []byte("OldPassword1!")) != nil {
			t.Fatalf("%s confirmation mutated password state", name)
		}
	}

	weak := request("Weak1!", "Weak1!")
	if weak.Code != http.StatusSeeOther || !strings.Contains(weak.Header().Get("Location"), "password+must+be+at+least+14+characters") {
		t.Fatalf("tenant policy response = (%d, %q)", weak.Code, weak.Header().Get("Location"))
	}
	if !client.User.GetX(ctx, u.ID).ForcePasswordChange {
		t.Fatal("policy failure cleared force-password-change state")
	}

	success := request("StrongNewPassword1!", "StrongNewPassword1!")
	if success.Code != http.StatusSeeOther || success.Header().Get("Location") != "/?flash=Password+changed" {
		t.Fatalf("success response = (%d, %q)", success.Code, success.Header().Get("Location"))
	}
	changed := client.User.GetX(ctx, u.ID)
	if changed.ForcePasswordChange || bcrypt.CompareHashAndPassword([]byte(changed.PasswordHash), []byte("StrongNewPassword1!")) != nil {
		t.Fatal("successful password change did not update password state")
	}
}
