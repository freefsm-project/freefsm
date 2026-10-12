package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestPasswordPolicySealedProspectiveAndExplicitForceHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Policy user").SetEmail("policy@test").SetRole("tech").SetPasswordHash("existing-legacy-hash").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	person := sessionCookie(t, ctx, sessions, u.ID)
	policy := url.Values{"password_min_length": {"16"}, "password_require_uppercase": {"on"}, "password_require_lowercase": {"on"}, "password_require_digit": {"on"}, "password_require_special": {"on"}}
	accountPost(t, router, person, "/settings/advanced", policy, 403)
	accountPost(t, router, cookie, "/settings/advanced", policy, 303)
	saved := client.User.GetX(ctx, u.ID)
	if saved.PasswordHash != "existing-legacy-hash" || saved.ForcePasswordChange {
		t.Fatal("prospective policy rewrote existing credential state")
	}
	if _, err := sessions.Validate(ctx, person.Value); err != nil {
		t.Fatal("prospective policy revoked a session")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='security.password.manage'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("policy audit=%d %v", count, err)
	}
	// Account security is explicit and independent from merely saving policy.
	accountPost(t, router, cookie, fmt.Sprintf("/users/%d/require-change", u.ID), nil, 303)
	if !client.User.GetX(ctx, u.ID).ForcePasswordChange {
		t.Fatal("explicit force missing")
	}
	if _, err := sessions.Validate(ctx, person.Value); err == nil {
		t.Fatal("force retained session")
	}
	// Protected identity, not a renamed Role, owns policy management.
	if _, err := authorization.New(pool).SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Security imitator", []authorization.Grant{{Permission: "security.password.manage", Scope: authorization.Sealed}}); err == nil {
		t.Fatal("sealed policy grant delegated")
	}
}

func TestRecoveryUnavailableDoesNotEnumerateHTTP(t *testing.T) {
	_, _, router, _, _, _ := permissionHTTPFixture(t)
	var body string
	for _, email := range []string{"admin@test", "unknown@test"} {
		r := httptest.NewRequest("POST", "/forgot-password", strings.NewReader(url.Values{"email": {email}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "Recovery email is unavailable") {
			t.Fatalf("unavailable email status=%d body=%s", w.Code, w.Body.String())
		}
		if body != "" && body != w.Body.String() {
			t.Fatal("unavailable response enumerates account")
		}
		body = w.Body.String()
	}
}

func TestSelfChangeAuditFailurePreservesAllStateHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	other := sessionCookie(t, ctx, sessions, admin)
	resets := services.NewPasswordResetService(client, pool)
	token, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	before := client.User.GetX(ctx, admin).PasswordHash
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_password_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user.password.change' THEN RAISE EXCEPTION 'injected password audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_password_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_password_audit()`); err != nil {
		t.Fatal(err)
	}
	accountPost(t, router, cookie, "/change-password", url.Values{"current_password": {"PermissionFixture1!"}, "new_password": {"SelfPasswordNew1!"}, "confirm_password": {"SelfPasswordNew1!"}}, 500)
	if client.User.GetX(ctx, admin).PasswordHash != before {
		t.Fatal("audit failure changed credential")
	}
	for _, c := range []string{cookie.Value, other.Value} {
		if _, err = sessions.Validate(ctx, c); err != nil {
			t.Fatal("audit failure revoked session")
		}
	}
	if _, err = resets.Validate(ctx, token); err != nil {
		t.Fatal("audit failure invalidated recovery")
	}
}

func TestPasswordPolicyRangeReviewAndAuditRollbackHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, minimum := range []string{"5", "73", "128"} {
		accountPost(t, router, cookie, "/settings/advanced", url.Values{"password_min_length": {minimum}}, 303)
		var value int
		if err := pool.QueryRow(ctx, `SELECT password_min_length FROM company_settings`).Scan(&value); err != nil || value != 8 {
			t.Fatalf("invalid policy persisted=%d %v", value, err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET password_min_length=128`); err != nil {
		t.Fatal(err)
	}
	body := requestBody(t, router, cookie, "GET", "/settings/advanced", 200)
	if !strings.Contains(body, "needs explicit Administrator review") || !strings.Contains(body, `max="72"`) {
		t.Fatal("legacy impossible policy has no actionable review")
	}
	if err := services.NewUserService(client).Authenticate(ctx, "admin@test", "PermissionFixture1!"); err != nil {
		t.Fatal("policy retroactively denied login")
	}
	for _, minimum := range []string{"6", "72"} {
		accountPost(t, router, cookie, "/settings/advanced", url.Values{"password_min_length": {minimum}}, 303)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_policy_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='security.password.manage' THEN RAISE EXCEPTION 'injected policy audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_policy_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_policy_audit()`); err != nil {
		t.Fatal(err)
	}
	accountPost(t, router, cookie, "/settings/advanced", url.Values{"password_min_length": {"8"}}, 500)
	var value int
	if err := pool.QueryRow(ctx, `SELECT password_min_length FROM company_settings`).Scan(&value); err != nil || value != 72 {
		t.Fatalf("policy audit failure partially committed: %d %v", value, err)
	}
}
