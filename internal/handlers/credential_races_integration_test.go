package handlers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/hostrecovery"
	"github.com/freefsm-project/freefsm/internal/services"
)

func credentialPost(router http.Handler, path string, values url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestSelfChangeAndRecoveryRaceHaveOneCredentialWinner(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	resets := services.NewPasswordResetService(client, pool)
	token, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	var blocker int
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	self, reset := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() {
		self <- credentialPost(router, "/change-password", url.Values{"current_password": {"PermissionFixture1!"}, "new_password": {"RaceSelfPassword1!"}, "confirm_password": {"RaceSelfPassword1!"}}, cookie)
	}()
	go func() { reset <- recoveryPost(router, token, "RaceResetPassword1!") }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err = pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, blocker).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("both credential actions did not reach the lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	sw, rw := <-self, <-reset
	selfWon := sw.Header().Get("Location") == "/?flash=Password+changed"
	resetWon := rw.Code == 303
	if selfWon == resetWon {
		t.Fatalf("winners self=%v reset=%v", selfWon, resetWon)
	}
	_, sessionErr := sessions.Validate(ctx, cookie.Value)
	if (sessionErr == nil) != selfWon {
		t.Fatalf("surviving session inconsistent with winner: %v", sessionErr)
	}
	if _, err = resets.Validate(ctx, token); err == nil {
		t.Fatal("stale proof survived credential race")
	}
}

func TestSetupAndHostPolicyRecheckAfterWaiting(t *testing.T) {
	for _, flow := range []string{"setup", "host"} {
		t.Run(flow, func(t *testing.T) {
			pool, client, _, sessions, _, admin := permissionHTTPFixture(t, false, flow == "setup")
			ctx := context.Background()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			var pid int
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if flow == "host" {
					done <- hostrecovery.ResetExistingAdmin(ctx, pool, "admin@test", "ShortPolicy1!", "1000")
					return
				}
				handler := NewSetupHandler(pool, sessions, services.NewUserService(client, pool), services.NewCompanySettingsService(client), &config.Config{SetupToken: "setup-secret"})
				w := credentialPost(http.HandlerFunc(handler.ServeHTTP), "/setup", url.Values{"token": {"setup-secret"}, "name": {"Initial"}, "email": {"initial@test"}, "password": {"ShortPolicy1!"}, "confirm_password": {"ShortPolicy1!"}}, nil)
				if w.Header().Get("Location") == "/setup/company" {
					done <- nil
					return
				}
				done <- fmt.Errorf("setup policy rejected")
			}()
			waitCredentialBlock(t, pool, pid)
			if _, err = tx.Exec(ctx, `UPDATE company_settings SET password_min_length=40`); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err == nil {
				t.Fatal("stale policy setter succeeded")
			}
			if flow == "setup" {
				if n := client.User.Query().CountX(ctx); n != 0 {
					t.Fatalf("stale policy created %d users", n)
				}
			} else {
				if client.User.GetX(ctx, admin).ForcePasswordChange {
					t.Fatal("failed host reset changed force state")
				}
				if err = services.NewUserService(client).Authenticate(ctx, "admin@test", "PermissionFixture1!"); err != nil {
					t.Fatal("failed host reset changed password")
				}
			}
		})
	}
}

func TestPasswordSettersRecheckPolicyAfterWaitingHTTP(t *testing.T) {
	for _, flow := range []string{"self", "admin-reset", "manual-create"} {
		t.Run(flow, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			target := client.User.Create().SetCompanyID(company).SetName("Target").SetEmail("target-policy@test").SetRole("tech").SetPasswordHash("original").SaveX(ctx)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			var pid int
			if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
				t.Fatal(err)
			}
			if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				switch flow {
				case "self":
					done <- credentialPost(router, "/change-password", url.Values{"current_password": {"PermissionFixture1!"}, "new_password": {"AfterPolicy1!"}, "confirm_password": {"AfterPolicy1!"}}, cookie)
				case "admin-reset":
					done <- credentialPost(router, fmt.Sprintf("/users/%d/reset-password", target.ID), url.Values{"password": {"AfterPolicy1!"}, "confirm_password": {"AfterPolicy1!"}}, cookie)
				case "manual-create":
					done <- credentialPost(router, "/users", url.Values{"name": {"New"}, "email": {"policy-new@test"}, "role_id": {"2"}, "password": {"AfterPolicy1!"}, "confirm_password": {"AfterPolicy1!"}}, cookie)
				}
			}()
			waitCredentialBlock(t, pool, pid)
			if _, err = tx.Exec(ctx, `UPDATE company_settings SET password_min_length=40`); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			response := <-done
			if response.Code != http.StatusInternalServerError || response.Header().Get("Location") != "" {
				t.Fatalf("stale policy falsely acknowledged: status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
			}
			if client.User.GetX(ctx, target.ID).PasswordHash != "original" {
				t.Fatal("stale policy changed target")
			}
			if err = services.NewUserService(client).Authenticate(ctx, "admin@test", "PermissionFixture1!"); err != nil {
				t.Fatal("stale policy changed actor")
			}
			if _, err = services.NewUserService(client).GetByEmail(ctx, "policy-new@test"); err == nil {
				t.Fatal("stale policy created account")
			}
		})
	}
}

func TestAdministratorResetRechecksRevokedAuthority(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	target := client.User.Create().SetCompanyID(company).SetName("Target").SetEmail("authority-target@test").SetRole("tech").SetPasswordHash("old").SaveX(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var pid int
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- credentialPost(router, fmt.Sprintf("/users/%d/reset-password", target.ID), url.Values{"password": {"AdminAfter1!"}, "confirm_password": {"AdminAfter1!"}}, cookie)
	}()
	waitCredentialBlock(t, pool, pid)
	if _, err = tx.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	response := <-done
	if response.Code != http.StatusInternalServerError || response.Header().Get("Location") != "" {
		t.Fatalf("revoked authority falsely acknowledged: status=%d location=%q body=%s", response.Code, response.Header().Get("Location"), response.Body.String())
	}
	if client.User.GetX(ctx, target.ID).PasswordHash != "old" {
		t.Fatal("revoked Administrator reset credential")
	}
	if err = authorization.New(pool).ResetAccountPassword(ctx, authorization.Actor{UserID: target.ID}, admin, "WrongActor1!"); err == nil {
		t.Fatal("nonprotected reset accepted")
	}
}
