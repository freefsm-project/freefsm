package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent/user"
	"github.com/freefsm-project/freefsm/internal/services"
	"golang.org/x/crypto/bcrypt"
)

// Inspect actual rendered controls rather than accepting unrelated policy text.
func assertPasswordFormMinimum(t *testing.T, body string, minimum int, ids ...string) {
	t.Helper()
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = false
	}
	for _, input := range regexp.MustCompile(`<input\b[^>]*>`).FindAllString(body, -1) {
		attrs := map[string]string{}
		for _, a := range regexp.MustCompile(`\b(id|type|minlength)="([^"]*)"`).FindAllStringSubmatch(input, -1) {
			attrs[a[1]] = a[2]
		}
		if _, ok := wanted[attrs["id"]]; !ok {
			continue
		}
		if attrs["type"] != "password" || attrs["minlength"] != strconv.Itoa(minimum) {
			t.Errorf("password control %s has type=%q minimum=%q; want password/%d", attrs["id"], attrs["type"], attrs["minlength"], minimum)
		}
		wanted[attrs["id"]] = true
	}
	for id, found := range wanted {
		if !found {
			t.Errorf("missing password control %s", id)
		}
	}
}

func TestComposedPolicyMinimumOnEveryPasswordSettingFormHTTP(t *testing.T) {
	t.Run("setup", func(t *testing.T) {
		pool, _, router, _, _, _ := permissionHTTPFixture(t, false, true)
		if _, err := pool.Exec(context.Background(), `UPDATE company_settings SET password_min_length=16`); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("GET", "/setup", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("setup form=%d", w.Code)
		}
		assertPasswordFormMinimum(t, w.Body.String(), 16, "setup-password", "setup-confirm-password")
	})
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET password_min_length=16`); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, form := range []struct {
		path string
		ids  []string
	}{
		{"/users/new", []string{"user-password", "user-confirm-password"}},
		{fmt.Sprintf("/users/%d", admin), []string{"admin-reset-password", "admin-reset-confirm-password"}},
		{"/change-password", []string{"new_password", "confirm_password"}},
	} {
		t.Run(form.path, func(t *testing.T) {
			assertPasswordFormMinimum(t, requestBody(t, router, cookie, "GET", form.path, 200), 16, form.ids...)
		})
	}
	_, invite, _ := pendingInvitationHTTP(t, pool, client, company, admin, "composed-ui")
	reset, err := services.NewPasswordResetService(client, pool).CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	for _, form := range []struct {
		path, token string
		ids         []string
	}{
		{"/accept-invite", invite, []string{"invite-password", "invite-confirm-password"}},
		{"/reset-password", reset, []string{"reset-password", "reset-confirm-password"}},
	} {
		t.Run(form.path, func(t *testing.T) {
			r := httptest.NewRequest("GET", form.path+"?token="+form.token, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("credential form=%d", w.Code)
			}
			assertPasswordFormMinimum(t, w.Body.String(), 16, form.ids...)
			for _, confirmation := range []string{"different", "TooShort1!"} {
				failed := onboardingPost(router, nil, form.path, url.Values{"token": {form.token}, "password": {"TooShort1!"}, "confirm_password": {confirmation}})
				if failed.Code != 200 {
					t.Fatalf("rejected form=%d", failed.Code)
				}
				assertPasswordFormMinimum(t, failed.Body.String(), 16, form.ids...)
			}
		})
	}
}

func TestComposedPendingAdministratorResetPreservesInvitationHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	pending, token, _ := pendingInvitationHTTP(t, pool, client, company, admin, "composed-pending")
	w := onboardingPost(router, sessionCookie(t, ctx, sessions, admin), fmt.Sprintf("/users/%d/reset-password", pending.ID), url.Values{"password": {"TemporaryPending1!"}, "confirm_password": {"TemporaryPending1!"}})
	if w.Code < 400 || w.Header().Get("Location") != "" {
		t.Fatalf("pending reset acknowledged: status=%d location=%q", w.Code, w.Header().Get("Location"))
	}
	got := client.User.GetX(ctx, pending.ID)
	if got.IsActive || got.ForcePasswordChange || got.OnboardingCompletedAt != nil || got.PasswordHash != pending.PasswordHash {
		t.Fatal("rejected pending reset altered onboarding state")
	}
	invites := services.NewInvitationService(client, pool)
	if target, err := invites.ValidateInvite(ctx, token); err != nil || target.UserID != pending.ID {
		t.Fatalf("rejected pending reset consumed proof: %v", err)
	}
	w = onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {"InvitedChosenPassword1!"}, "confirm_password": {"InvitedChosenPassword1!"}})
	if w.Code != 303 || !client.User.GetX(ctx, pending.ID).IsActive {
		t.Fatalf("preserved invitation unusable: %d", w.Code)
	}
}

func TestComposedInvitationAndManualBcryptBoundaryHTTP(t *testing.T) {
	for _, flow := range []string{"invitation", "manual"} {
		t.Run(flow, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			if _, err := pool.Exec(ctx, `UPDATE company_settings SET password_min_length=16`); err != nil {
				t.Fatal(err)
			}
			exact := "Aa1!" + strings.Repeat("x", 68)
			tooLong := exact + "x"
			if flow == "invitation" {
				pending, token, _ := pendingInvitationHTTP(t, pool, client, company, admin, "composed-bytes")
				w := onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {tooLong}, "confirm_password": {tooLong}})
				if w.Code != 200 || !strings.Contains(w.Body.String(), "72 UTF-8 bytes") {
					t.Fatalf("oversize invitation response=%d %s", w.Code, w.Body.String())
				}
				if got := client.User.GetX(ctx, pending.ID); got.PasswordHash != pending.PasswordHash || got.IsActive {
					t.Fatal("oversize invitation changed account")
				}
				// Exercise the transaction boundary directly as well as handler validation.
				if _, err := services.NewInvitationService(client, pool).AcceptInvite(ctx, token, tooLong); !errors.Is(err, services.ErrInvitationPassword) || !strings.Contains(err.Error(), "72 UTF-8 bytes") {
					t.Fatalf("transaction did not use shared policy error for oversize input: %v", err)
				}
				if _, err := services.NewInvitationService(client, pool).ValidateInvite(ctx, token); err != nil {
					t.Fatal("byte rejection consumed token")
				}
				w = onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {exact}, "confirm_password": {exact}})
				got := client.User.GetX(ctx, pending.ID)
				if w.Code != 303 || !got.IsActive || got.ForcePasswordChange || bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte(exact)) != nil {
					t.Fatalf("exact-byte invite failed: %d", w.Code)
				}
			} else {
				cookie := sessionCookie(t, ctx, sessions, admin)
				values := url.Values{"name": {"Manual byte limit"}, "email": {"composed-manual@example.test"}, "role_id": {"2"}, "password": {tooLong}, "confirm_password": {tooLong}}
				w := onboardingPost(router, cookie, "/users", values)
				if w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "72+UTF-8+bytes") {
					t.Fatalf("oversize manual response=%d %q", w.Code, w.Header().Get("Location"))
				}
				if client.User.Query().Where(user.EmailEQ(values.Get("email"))).ExistX(ctx) {
					t.Fatal("oversize manual account created")
				}
				// The shared creation transaction must not trust a supplied hash over plaintext.
				_, err := authorization.New(pool).CreateAccount(ctx, authorization.Actor{UserID: admin}, authorization.NewAccount{Name: "Boundary", Email: "boundary-direct@example.test", RoleID: 2, Password: tooLong, PasswordHash: "not-authoritative"})
				if err == nil {
					t.Fatal("shared manual boundary skipped plaintext validation")
				}
				values.Set("password", exact)
				values.Set("confirm_password", exact)
				w = onboardingPost(router, cookie, "/users", values)
				got := client.User.Query().Where(user.EmailEQ(values.Get("email"))).OnlyX(ctx)
				if w.Code != http.StatusSeeOther || !got.IsActive || !got.ForcePasswordChange || bcrypt.CompareHashAndPassword([]byte(got.PasswordHash), []byte(exact)) != nil {
					t.Fatalf("exact-byte manual password failed: %d", w.Code)
				}
			}
		})
	}
}
