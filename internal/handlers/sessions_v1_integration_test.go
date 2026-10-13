package handlers

import (
	"context"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOwnSessionsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Session owner").SetEmail("sessions@test").SetRole("tech").SetPasswordHash("unused").SaveX(ctx)
	own := sessionCookie(t, ctx, sessions, u.ID)
	other := sessionCookie(t, ctx, sessions, admin)
	mobile, err := sessions.CreateMobile(ctx, u.ID, "Field tablet")
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	w := request("GET", "/sessions", own)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Field tablet") || !strings.Contains(w.Body.String(), "Current session") {
		t.Fatalf("own list: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), mobile) || strings.Contains(w.Body.String(), own.Value) {
		t.Fatal("bearer disclosed")
	}
	if w := request("GET", fmt.Sprintf("/users/%d/sessions", admin), own); w.Code != 403 && w.Code != 404 {
		t.Fatalf("other user list=%d", w.Code)
	}
	if w := request("GET", fmt.Sprintf("/users/%d/sessions", u.ID), other); w.Code != 200 {
		t.Fatalf("sealed list=%d", w.Code)
	}
	service := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	role, err := service.SaveRole(ctx, actor, 0, "No directory", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.AssignUser(ctx, actor, u.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SaveRole(ctx, actor, role, "Renamed empty Role", nil, 1); err != nil {
		t.Fatal(err)
	}
	replacement, err := service.SaveRole(ctx, actor, 0, "Replacement", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.RemoveRole(ctx, actor, role, replacement); err != nil {
		t.Fatal(err)
	}
	if w := request("GET", "/sessions", own); w.Code != 200 {
		t.Fatalf("own anchor lost after Role replacement: %d", w.Code)
	}
	if w := request("GET", "/users", own); w.Code != 403 {
		t.Fatalf("intrinsic sessions granted directory access: %d", w.Code)
	}
	if w := request("POST", "/sessions/revoke-all", own); w.Code != 303 {
		t.Fatalf("revoke all=%d %s", w.Code, w.Body.String())
	}
	if _, err := sessions.Validate(ctx, own.Value); err == nil {
		t.Fatal("browser survived")
	}
	if _, err := sessions.ValidateMobile(ctx, mobile); err == nil {
		t.Fatal("mobile survived")
	}
	if _, err := sessions.Validate(ctx, other.Value); err != nil {
		t.Fatal("other user revoked")
	}
}

func TestSessionRevocationFencesInflightAPIAction(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "Racing phone")
	if err != nil {
		t.Fatal(err)
	}
	entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(time.Now().Add(-time.Hour)).SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		r := httptest.NewRequest("POST", "/time-entries/clock-out", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		done <- w
	}()
	waitCredentialBlock(t, pool, pid)
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	w := <-done
	if w.Code >= 200 && w.Code < 300 {
		t.Fatalf("revoked action committed: %d", w.Code)
	}
	if client.TimeEntry.GetX(ctx, entry.ID).ClockOut != nil {
		t.Fatal("in-flight revoked action changed time entry")
	}
	if _, err := sessions.ValidateMobile(ctx, token); err == nil {
		t.Fatal("renewal revived revoked token")
	}
}

func TestSessionRevocationAuditFailureAndDuplicateHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	target, err := sessions.CreateMobile(ctx, admin, "Audit phone")
	if err != nil {
		t.Fatal(err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM sessions WHERE device_name='Audit phone'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	call := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", fmt.Sprintf("/sessions/%d/revoke", id), nil)
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_session_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user.sessions.revoke' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_session_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_session_audit()`); err != nil {
		t.Fatal(err)
	}
	if w := call(); w.Code < 400 {
		t.Fatal("audit failure acknowledged")
	}
	if _, err := sessions.ValidateMobile(ctx, target); err != nil {
		t.Fatal("audit failure revoked session")
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER reject_session_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if w := call(); w.Code != 303 {
			t.Fatalf("revocation=%d", w.Code)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='user.sessions.revoke'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate audit=%d %v", count, err)
	}
	// Reconstructing the service observes durable revocation, not process state.
	list, err := authorization.New(pool).Sessions(ctx, authorization.Actor{UserID: admin}, admin)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range list {
		if s.ID == id && s.RevokedAt != nil {
			found = true
		}
	}
	if !found {
		t.Fatal("revocation evidence lost after service restart")
	}
}

func TestMobileAuthorizationExpiryHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "Expiry phone")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(path string) int {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w.Code
	}
	var before, after time.Time
	if err := pool.QueryRow(ctx, `UPDATE sessions SET last_used_at=clock_timestamp()-interval '1 minute',expires_at=clock_timestamp()+interval '1 hour' WHERE device_name='Expiry phone' RETURNING expires_at`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if code := call("/jobs/9223372036854775807"); code != 404 {
		t.Fatalf("failed authorization=%d", code)
	}
	if err := pool.QueryRow(ctx, `SELECT expires_at FROM sessions WHERE device_name='Expiry phone'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatal("failed authorization renewed mobile")
	}
	if code := call("/me"); code != 200 {
		t.Fatalf("me=%d", code)
	}
	if err := pool.QueryRow(ctx, `SELECT expires_at FROM sessions WHERE device_name='Expiry phone'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if time.Until(after) < 30*24*time.Hour-time.Minute {
		t.Fatal("successful authorization inside five minutes did not renew")
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp() WHERE device_name='Expiry phone'`); err != nil {
		t.Fatal(err)
	}
	if code := call("/me"); code != 401 {
		t.Fatalf("expired session=%d", code)
	}
}

func TestSessionAPIRecoveryDispositionHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "Restore phone")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(epoch string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/sessions", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("X-FreeFSM-Session-Epoch", epoch)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	first := call("")
	if first.Code != 200 {
		t.Fatalf("own API list=%d", first.Code)
	}
	epoch := first.Header().Get("X-FreeFSM-Session-Epoch")
	if epoch == "" {
		t.Fatal("missing recovery epoch")
	}
	if err := sessions.RevokeMobile(ctx, token); err != nil {
		t.Fatal(err)
	}
	ordinary := call(epoch)
	if ordinary.Code != 401 || !strings.Contains(ordinary.Body.String(), `"recovery_disposition":"purge"`) {
		t.Fatalf("ordinary=%d %s", ordinary.Code, ordinary.Body.String())
	}
	if _, err := pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid()`); err != nil {
		t.Fatal(err)
	}
	restored := call(epoch)
	if restored.Code != 401 || !strings.Contains(restored.Body.String(), `"recovery_disposition":"locked_review"`) {
		t.Fatalf("restored=%d %s", restored.Code, restored.Body.String())
	}
}

func TestSessionRenewalCannotUndoConcurrentRevocation(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "Concurrent renewal")
	if err != nil {
		t.Fatal(err)
	}
	var expiry time.Time
	if err := pool.QueryRow(ctx, `SELECT expires_at FROM sessions WHERE device_name='Concurrent renewal'`).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	done := make(chan int, 1)
	go func() {
		r := httptest.NewRequest("GET", "/me", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		done <- w.Code
	}()
	waitCredentialBlock(t, pool, pid)
	if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE device_name='Concurrent renewal'`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if code := <-done; code != 200 {
		t.Fatalf("successful read preceding revocation=%d", code)
	}
	var unchanged bool
	if err := pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL AND expires_at=$1 FROM sessions WHERE device_name='Concurrent renewal'`, expiry).Scan(&unchanged); err != nil || !unchanged {
		t.Fatalf("renewal undid revocation: %v %v", unchanged, err)
	}
	if _, err := sessions.ValidateMobile(ctx, token); err == nil {
		t.Fatal("revoked session valid")
	}
}

func TestBrowserFixedExpiryAndDuplicateLogoutHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateAuthenticated(ctx, admin, "PermissionFixture1!", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	var fixed bool
	if err := pool.QueryRow(ctx, `SELECT expires_at-created_at=interval '7 days' FROM sessions WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, admin).Scan(&fixed); err != nil || !fixed {
		t.Fatalf("browser deadline not exactly seven days: %v %v", fixed, err)
	}
	call := func(method, path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.AddCookie(&http.Cookie{Name: "session", Value: token})
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	if w := call("GET", "/sessions"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if err := pool.QueryRow(ctx, `SELECT expires_at-created_at=interval '7 days' FROM sessions WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, admin).Scan(&fixed); err != nil || !fixed {
		t.Fatal("browser renewed on access")
	}
	if w := call("POST", "/logout"); w.Code != 303 {
		t.Fatalf("logout=%d", w.Code)
	}
	if w := call("POST", "/logout"); w.Code != 401 {
		t.Fatalf("duplicate logout=%d", w.Code)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='user.sessions.logout'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("logout audit=%d %v", n, err)
	}
	token, err = sessions.CreateAuthenticated(ctx, admin, "PermissionFixture1!", "web", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET expires_at=clock_timestamp() WHERE user_id=$1 AND revoked_at IS NULL`, admin); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/preferences"); w.Code != 401 || !strings.Contains(w.Body.String(), "Session expired—sign in again") {
		t.Fatalf("expiry=%d %s", w.Code, w.Body.String())
	}
}
