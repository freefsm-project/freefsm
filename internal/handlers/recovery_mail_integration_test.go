package handlers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/hostrecovery"
	"github.com/freefsm-project/freefsm/internal/services"
)

var recoveryRequestSequence atomic.Uint32

func requestRecoveryHTTP(router http.Handler, email string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/forgot-password", strings.NewReader(url.Values{"email": {email}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", recoveryRequestSequence.Add(1))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

type stalledRecoverySMTP struct {
	net.Listener
	entered chan struct{}
	release chan struct{}
}

func (s stalledRecoverySMTP) Accept() (net.Conn, error) {
	c, e := s.Listener.Accept()
	if e == nil {
		close(s.entered)
		<-s.release
	}
	return c, e
}

func TestRecoveryDurableDispatchNonEnumerationHTTP(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint("SMTP failure=", failure), func(t *testing.T) {
			pool, client, router, _, company, _ := permissionHTTPFixture(t)
			ctx := context.Background()
			client.User.Create().SetCompanyID(company).SetEmail("disabled@test").SetName("Disabled").SetRole("tech").SetIsActive(false).SetPasswordHash("old").SaveX(ctx)
			client.User.Create().SetCompanyID(company).SetEmail("pending@test").SetName("Pending").SetRole("tech").SetIsActive(false).SetPasswordHash("unusable").SetWelcomeEmailSentAt(time.Now()).SaveX(ctx)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if _, err = pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='recovery@example.test'`, listener.Addr().(*net.TCPAddr).Port); err != nil {
				t.Fatal(err)
			}
			var body string
			for _, address := range []string{"admin@test", "unknown@test", "disabled@test", "pending@test"} {
				w := requestRecoveryHTTP(router, address)
				if w.Code != 200 {
					t.Fatalf("request %s: %d", address, w.Code)
				}
				if body != "" && body != w.Body.String() {
					t.Fatal("public response enumerates")
				}
				body = w.Body.String()
			}
			var queued int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM password_recovery_requests WHERE completed_at IS NULL`).Scan(&queued); err != nil || queued != 4 {
				t.Fatalf("durable queue=%d %v", queued, err)
			}
			// A fresh service resumes the persisted queue, independent of the HTTP handler.
			resets := services.NewPasswordResetService(client, pool)
			mail := services.NewEmailService(services.NewCompanySettingsService(client))
			stalled := stalledRecoverySMTP{listener, make(chan struct{}), make(chan struct{})}
			messages := make(chan []byte, 1)
			smtpDone := make(chan error, 1)
			go func() {
				if failure {
					c, e := stalled.Accept()
					if e == nil {
						_, e = io.WriteString(c, "421 unavailable\r\n")
						c.Close()
					}
					smtpDone <- e
					return
				}
				m, e := receiveTestSMTP(stalled)
				messages <- m
				smtpDone <- e
			}()
			done := make(chan error, 1)
			go func() { _, e := resets.DispatchOne(ctx, mail); done <- e }()
			select {
			case <-stalled.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("SMTP worker did not start")
			}
			// SMTP is provably blocked; all addresses must still finish their HTTP path.
			responses := make(chan *httptest.ResponseRecorder, 4)
			go func() {
				for _, a := range []string{"admin@test", "unknown@test", "disabled@test", "pending@test"} {
					responses <- requestRecoveryHTTP(router, a)
				}
			}()
			for i := 0; i < 4; i++ {
				select {
				case w := <-responses:
					if w.Code != 200 || w.Body.String() != body {
						t.Fatal("response changed during SMTP wait")
					}
				case <-time.After(2 * time.Second):
					close(stalled.release)
					t.Fatal("public request waited for account-specific SMTP")
				}
			}
			close(stalled.release)
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			if err = <-smtpDone; err != nil {
				t.Fatal(err)
			}
			if !failure {
				m := <-messages
				match := regexp.MustCompile(`token=([0-9a-f]{64})`).FindSubmatch(m)
				if len(match) != 2 {
					t.Fatalf("no recovery URL in real SMTP message: %s", m)
				}
				if w := recoveryPost(router, string(match[1]), "FromRealSMTP1!"); w.Code != 303 {
					t.Fatalf("mailed link failed: %d", w.Code)
				}
			}
			// Unknown/disabled/pending claims complete without a message or usable link.
			for i := 0; i < 3; i++ {
				if _, err = resets.DispatchOne(ctx, mail); err != nil {
					t.Fatal(err)
				}
			}
			var unsafe int
			if err = pool.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens t JOIN users u ON u.id=t.user_id WHERE NOT u.is_active AND t.purpose='password-recovery-v1'`).Scan(&unsafe); err != nil || unsafe != 0 {
				t.Fatalf("inactive recovery proofs=%d %v", unsafe, err)
			}
		})
	}
}

func TestRecoveryEmailIdentityBindingAndRetainedLegacy(t *testing.T) {
	pool, client, router, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	svc := authorization.New(pool)
	u := client.User.Create().SetCompanyID(company).SetName("Identity").SetEmail("old-mailbox@test").SetRole("tech").SetPasswordHash("old").SaveX(ctx)
	resets := services.NewPasswordResetService(client, pool)
	token, err := resets.CreateToken(ctx, u.ID, u.Email)
	if err != nil {
		t.Fatal(err)
	}
	if err = resets.Request(ctx, u.Email, "http://localhost/reset-password"); err != nil {
		t.Fatal(err)
	}
	if err = svc.EditIdentity(ctx, authorization.Actor{UserID: admin}, u.ID, u.Name, "new-mailbox@test"); err != nil {
		t.Fatal(err)
	}
	if _, err = resets.CreateToken(ctx, u.ID, u.Email); err == nil {
		t.Fatal("stale destination issued proof")
	}
	if w := recoveryPost(router, token, "OldMailboxAttack1!"); w.Code == 303 {
		t.Fatal("superseded mailbox proof redeemed")
	}
	var changed bool
	if err = pool.QueryRow(ctx, `SELECT q.credential_revision<>u.credential_revision FROM password_recovery_requests q JOIN users u ON u.id=q.user_id WHERE u.id=$1`, u.ID).Scan(&changed); err != nil || !changed {
		t.Fatalf("queued intent not revision-bound: %v %v", changed, err)
	}
	// Administrative/password/disable actions retain ambiguous legacy evidence.
	if _, err = pool.Exec(ctx, `INSERT INTO password_reset_tokens(user_id,company_id,token_hash,expires_at) VALUES($1,$2,'legacy-evidence',clock_timestamp())`, u.ID, company); err != nil {
		t.Fatal(err)
	}
	if err = svc.ResetAccountPassword(ctx, authorization.Actor{UserID: admin}, u.ID, "TemporaryIdentity1!"); err != nil {
		t.Fatal(err)
	}
	if err = svc.SetEnabled(ctx, authorization.Actor{UserID: admin}, u.ID, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM password_recovery_review WHERE user_id=$1`, u.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("legacy review lost=%d %v", count, err)
	}
}

func TestHostRecoveryRetainsLegacyReviewEvidence(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	resets := services.NewPasswordResetService(client, pool)
	token, err := resets.CreateToken(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err = pool.Exec(ctx, `INSERT INTO password_reset_tokens(company_id,user_id,token_hash,expires_at) VALUES($1,$2,'unknown-host-evidence',clock_timestamp())`, company, admin); err != nil {
		t.Fatal(err)
	}
	if err = hostrecovery.ResetExistingAdmin(ctx, pool, "admin@test", "HostReplacement1!", "1000"); err != nil {
		t.Fatal(err)
	}
	if _, err = resets.Validate(ctx, token); err == nil {
		t.Fatal("host reset retained usable proof")
	}
	if _, err = sessions.Validate(ctx, cookie.Value); err == nil {
		t.Fatal("host reset retained session")
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM password_recovery_review WHERE user_id=$1`, admin).Scan(&n); err != nil || n != 1 {
		t.Fatalf("host erased unknown evidence: %d %v", n, err)
	}
}
