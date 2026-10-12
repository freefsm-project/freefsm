package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/freefsm-project/freefsm/internal/services"
)

func recoveryPost(router http.Handler, token, password string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/reset-password", strings.NewReader(url.Values{"token": {token}, "password": {password}, "confirm_password": {password}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestRecoveryDisableReenableDoesNotReviveLink(t *testing.T) {
	pool, client, router, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Target").SetEmail("disabled@test").SetRole("tech").SetPasswordHash("old").SaveX(ctx)
	svc := authorization.New(pool)
	resets := services.NewPasswordResetService(client, pool)
	token, err := resets.CreateToken(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.SetEnabled(ctx, authorization.Actor{UserID: admin}, u.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = svc.SetEnabled(ctx, authorization.Actor{UserID: admin}, u.ID, true); err != nil {
		t.Fatal(err)
	}
	if w := recoveryPost(router, token, "RecoveryAfter1!"); w.Code == 303 {
		t.Fatal("disablement revived stale recovery proof")
	}
}

func TestRecoveryConcurrentRedemptionAndFailureHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	resets := services.NewPasswordResetService(client, pool)
	token, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	// A real storage fault at immutable audit insertion must roll back all effects.
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_recovery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user.password.recover' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_recovery BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_recovery_audit()`); err != nil {
		t.Fatal(err)
	}
	if w := recoveryPost(router, token, "RecoveryAfter1!"); w.Code == 303 {
		t.Fatal("audit failure acknowledged")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(client.User.GetX(ctx, admin).PasswordHash), []byte("PermissionFixture1!")); err != nil {
		t.Fatal("audit failure changed password")
	}
	if _, err = sessions.Validate(ctx, cookie.Value); err != nil {
		t.Fatal("audit failure revoked usable session")
	}
	if _, err = resets.Validate(ctx, token); err != nil {
		t.Fatal("audit failure consumed proof")
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_recovery ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- recoveryPost(router, token, "RecoveryAfter1!").Code }()
	}
	wg.Wait()
	close(codes)
	successes := 0
	for code := range codes {
		if code == 303 {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent redemption successes=%d", successes)
	}
}

func TestSelfChangeOnlyActingBrowserSurvivesHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	acting := sessionCookie(t, ctx, sessions, admin)
	other := sessionCookie(t, ctx, sessions, admin)
	mobile, err := sessions.CreateMobile(ctx, admin, "phone")
	if err != nil {
		t.Fatal(err)
	}
	resets := services.NewPasswordResetService(client, pool)
	token, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	accountPost(t, router, acting, "/change-password", url.Values{"current_password": {"PermissionFixture1!"}, "new_password": {"SelfChangeAfter1!"}, "confirm_password": {"SelfChangeAfter1!"}}, 303)
	if _, err = sessions.Validate(ctx, acting.Value); err != nil {
		t.Fatal("acting browser lost")
	}
	if _, err = sessions.Validate(ctx, other.Value); err == nil {
		t.Fatal("other browser survived")
	}
	if _, err = sessions.ValidateMobile(ctx, mobile); err == nil {
		t.Fatal("mobile survived")
	}
	if _, err = resets.Validate(ctx, token); err == nil {
		t.Fatal("recovery survived")
	}
	if _, err = sessions.CreateAuthenticated(ctx, admin, "PermissionFixture1!", "web", ""); err == nil {
		t.Fatal("stale login created session")
	}
}

func TestRecoveryPurposeExpiryAndPolicyHTTP(t *testing.T) {
	pool, client, router, _, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	resets := services.NewPasswordResetService(client, pool)
	for _, purpose := range []string{"invitation", "legacy-unknown", "password-recovery-v1"} {
		token, err := resets.CreateToken(ctx, admin)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256([]byte(token))
		if purpose == "password-recovery-v1" {
			if _, err = pool.Exec(ctx, `UPDATE password_reset_tokens SET expires_at=clock_timestamp() WHERE token_hash=$1`, hex.EncodeToString(hash[:])); err != nil {
				t.Fatal(err)
			}
		} else {
			if _, err = pool.Exec(ctx, `UPDATE password_reset_tokens SET purpose=$2 WHERE token_hash=$1`, hex.EncodeToString(hash[:]), purpose); err != nil {
				t.Fatal(err)
			}
		}
		if w := recoveryPost(router, token, "RecoveryAfter1!"); w.Code == 303 {
			t.Fatalf("accepted %s", purpose)
		}
	}
	token, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	var seconds float64
	if err = pool.QueryRow(ctx, `SELECT extract(epoch FROM expires_at-created_at) FROM password_reset_tokens WHERE purpose='password-recovery-v1' AND user_id=$1`, admin).Scan(&seconds); err != nil || seconds != 3600 {
		t.Fatalf("lifetime=%v err=%v", seconds, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE company_settings SET password_min_length=20`); err != nil {
		t.Fatal(err)
	}
	if w := recoveryPost(router, token, "RecoveryAfter1!"); w.Code == 303 {
		t.Fatal("stale policy accepted")
	}
	r := httptest.NewRequest("GET", "/reset-password?token="+token, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `minlength="20"`) {
		t.Fatal("current policy absent from reset UI")
	}
	if w := recoveryPost(router, token, "RecoveryAfterVeryLong1!"); w.Code != 303 {
		t.Fatalf("valid policy: %d %s", w.Code, w.Body.String())
	}
	t.Log(fmt.Sprintf("exact recovery lifetime %.0f seconds; wrong purposes and expiry denied", seconds))
}

func TestRecoveryNewestAndRevocationHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	resets := services.NewPasswordResetService(client, pool)
	old, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	newest, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if w := recoveryPost(router, old, "RecoveryNew1!"); w.Code == 303 {
		t.Fatal("replaced link changed credential")
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	mobile, err := sessions.CreateMobile(ctx, admin, "phone")
	if err != nil {
		t.Fatal(err)
	}
	if w := recoveryPost(router, newest, "RecoveryNew1!"); w.Code != 303 {
		t.Fatalf("reset: %d %s", w.Code, w.Body.String())
	}
	if _, err := sessions.Validate(ctx, cookie.Value); err == nil {
		t.Fatal("browser session survived reset")
	}
	if _, err := sessions.ValidateMobile(ctx, mobile); err == nil {
		t.Fatal("mobile session survived reset")
	}
	api := apiv1.NewRouter(pool, client, sessions)
	req := httptest.NewRequest("GET", "/me", nil)
	req.Header.Set("Authorization", "Bearer "+mobile)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, req)
	if response.Code != 401 {
		t.Fatalf("revoked bearer API status=%d", response.Code)
	}
	if w := recoveryPost(router, newest, "ReplayPassword1!"); w.Code == 303 {
		t.Fatal("replayed reset succeeded")
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='user.password.recover'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit=%d %v", n, err)
	}
}
