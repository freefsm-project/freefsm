package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/services"
)

func TestRecoveryBearerResponseProtectionHTTP(t *testing.T) {
	pool, client, router, _, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	resets := services.NewPasswordResetService(client, pool)
	token, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	requestNumber := 0
	request := func(t *testing.T, method, proof, password, confirmation string) *httptest.ResponseRecorder {
		t.Helper()
		target := "/reset-password?token=" + url.QueryEscape(proof)
		form := url.Values{"token": {proof}, "password": {password}, "confirm_password": {confirmation}}
		r := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
		// Isolate response branches from the public per-IP abuse budget.
		requestNumber++
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:12345", requestNumber)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		for name, want := range map[string]string{"Referrer-Policy": "no-referrer", "Cache-Control": "no-store"} {
			if got := w.Header().Get(name); got != want {
				t.Errorf("%s %s: %s=%q, want %q", method, t.Name(), name, got, want)
			}
		}
		return w
	}
	for _, tc := range []struct {
		name, method, proof, password, confirmation string
		status                                      int
	}{
		{"valid page", "GET", token, "", "", http.StatusOK},
		{"invalid page", "GET", "invalid", "", "", http.StatusOK},
		{"invalid submission", "POST", "invalid", "RecoveryAfter1!", "RecoveryAfter1!", http.StatusBadRequest},
		{"confirmation error", "POST", token, "RecoveryAfter1!", "different", http.StatusOK},
		{"policy error", "POST", token, "weak", "weak", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if w := request(t, tc.method, tc.proof, tc.password, tc.confirmation); w.Code != tc.status {
				t.Fatalf("status=%d want %d", w.Code, tc.status)
			}
		})
	}
	if _, err = pool.Exec(ctx, `CREATE FUNCTION reject_protection_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user.password.recover' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_protection BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_protection_audit()`); err != nil {
		t.Fatal(err)
	}
	if w := request(t, "POST", token, "RecoveryAfter1!", "RecoveryAfter1!"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Unable to reset password") {
		t.Fatal("storage failure did not render protected error")
	}
	if _, err = resets.Validate(ctx, token); err != nil {
		t.Fatalf("failed attempts consumed token: %v", err)
	}
	if _, err = pool.Exec(ctx, `DROP TRIGGER reject_protection ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	if w := request(t, "POST", token, "RecoveryAfter1!", "RecoveryAfter1!"); w.Code != http.StatusSeeOther {
		t.Fatalf("successful reset status=%d", w.Code)
	}
	if w := request(t, "POST", token, "RecoveryAfter1!", "RecoveryAfter1!"); w.Code != http.StatusBadRequest {
		t.Fatalf("replay status=%d", w.Code)
	}
}
