package handlers

import (
	"context"
	"fmt"
	"net"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestRecoveryIdentityChangeWhileIssuanceWaitsHTTP(t *testing.T) {
	for _, adminRequest := range []bool{false, true} {
		t.Run(fmt.Sprint("Administrator=", adminRequest), func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			u := client.User.Create().SetCompanyID(company).SetName("Old").SetEmail("old-race@test").SetPasswordHash("old").SetRole("tech").SaveX(ctx)
			client.CompanySettings.Update().SetSMTPHost("127.0.0.1").SetSMTPPort(12345).SetSMTPFrom("sender@example.test").SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, admin)
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
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				if adminRequest {
					done <- credentialPost(router, fmt.Sprintf("/users/%d/resend-recovery", u.ID), nil, cookie)
				} else {
					done <- requestRecoveryHTTP(router, u.Email)
				}
			}()
			waitCredentialBlock(t, pool, pid)
			if _, err = tx.Exec(ctx, `UPDATE users SET email='new-race@test' WHERE id=$1`, u.ID); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			<-done
			var n int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens WHERE user_id=$1 AND purpose='password-recovery-v1'`, u.ID).Scan(&n); err != nil || n != 0 {
				t.Fatalf("stale identity issued %d tokens: %v", n, err)
			}
			if !adminRequest {
				if err = pool.QueryRow(ctx, `SELECT count(*) FROM password_recovery_requests WHERE user_id IS NOT NULL`).Scan(&n); err != nil || n != 0 {
					t.Fatalf("stale public request bound new identity: %d %v", n, err)
				}
			}
		})
	}
}

func TestAdministratorInFlightRecoveryCannotRedeemAfterEmailEdit(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u := client.User.Create().SetCompanyID(company).SetName("Old recipient").SetEmail("old-inflight@test").SetPasswordHash("original").SetRole("tech").SaveX(ctx)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client.CompanySettings.Update().SetSMTPHost("127.0.0.1").SetSMTPPort(listener.Addr().(*net.TCPAddr).Port).SetSMTPFrom("sender@example.test").SaveX(ctx)
	delayed := delayedRecoverySMTP{listener, make(chan struct{}), make(chan struct{})}
	message := make(chan []byte, 1)
	go func() { b, _ := receiveTestSMTP(delayed); message <- b }()
	cookie := sessionCookie(t, ctx, sessions, admin)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- credentialPost(router, fmt.Sprintf("/users/%d/resend-recovery", u.ID), nil, cookie) }()
	select {
	case <-delayed.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Administrator SMTP not reached")
	}
	if err = authorization.New(pool).EditIdentity(ctx, authorization.Actor{UserID: admin}, u.ID, u.Name, "new-inflight@test"); err != nil {
		t.Fatal(err)
	}
	close(delayed.release)
	<-done
	match := regexp.MustCompile(`token=([a-f0-9]{64})`).FindSubmatch(<-message)
	if len(match) != 2 {
		t.Fatal("no SMTP proof")
	}
	if _, err = services.NewPasswordResetService(client, pool).Validate(ctx, string(match[1])); err == nil {
		t.Fatal("old mailbox recovered changed identity")
	}
	if w := recoveryPost(router, string(match[1]), "OldMailboxAttack1!"); w.Code == 303 {
		t.Fatal("old mailbox credential change succeeded")
	}
}
