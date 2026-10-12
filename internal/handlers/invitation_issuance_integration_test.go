package handlers

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/ent/user"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

func waitOnboardingFence(t *testing.T, pool *pgxpool.Pool, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting bool
		if err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("request did not reach deterministic database fence")
}

func TestInvitationUncertainSMTPAcceptanceIsTruthfulHTTP(t *testing.T) {
	for _, resend := range []bool{false, true} {
		t.Run(fmt.Sprint("resend=", resend), func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.test'`, listener.Addr().(*net.TCPAddr).Port); err != nil {
				t.Fatal(err)
			}
			message := make(chan []byte, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					message <- nil
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				p := textproto.NewConn(conn)
				_ = p.PrintfLine("220 test SMTP")
				for {
					line, err := p.ReadLine()
					if err != nil {
						message <- nil
						return
					}
					if line == "DATA" {
						_ = p.PrintfLine("354 send message")
						body, _ := p.ReadDotBytes()
						message <- body
						return
					}
					_ = p.PrintfLine("250 OK")
				}
			}()
			cookie := sessionCookie(t, ctx, sessions, admin)
			path := "/users"
			values := url.Values{"name": {"Uncertain mail"}, "email": {"uncertain21@example.test"}, "role_id": {"3"}, "send_welcome_email": {"on"}}
			if resend {
				u, _, _ := pendingInvitationHTTP(t, pool, client, company, admin, "uncertain21")
				path = fmt.Sprintf("/users/%d/resend-welcome", u.ID)
				values = nil
			}
			w := onboardingPost(router, cookie, path, values)
			location, _ := url.QueryUnescape(w.Header().Get("Location"))
			if w.Code != 303 || !strings.Contains(location, "uncertain") || !strings.Contains(location, "before resending") || strings.Contains(location, "was not accepted") {
				t.Fatalf("unknown DATA acknowledgement misrepresented: %d %s", w.Code, location)
			}
			body := <-message
			match := regexp.MustCompile(`token=([a-f0-9]{64})`).FindSubmatch(body)
			if len(match) != 2 {
				t.Fatal("server did not receive complete invitation DATA")
			}
			if strings.Contains(location, string(match[1])) {
				t.Fatal("outcome exposed invitation secret")
			}
			if strings.Contains(logs.String(), string(match[1])) {
				t.Fatal("handler logs exposed invitation secret")
			}
			u := client.User.Query().Where(user.EmailEQ("uncertain21@example.test")).OnlyX(ctx)
			if u.IsActive || u.WelcomeEmailSentAt != nil {
				t.Fatal("uncertain handoff invented acceptance or activation")
			}
			if _, err := services.NewInvitationService(client, pool).ValidateInvite(ctx, string(match[1])); err != nil {
				t.Fatal("uncertain but possibly delivered link was invalidated", err)
			}
			var leaked bool
			if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE detail::text LIKE '%'||$1||'%')`, string(match[1])).Scan(&leaked); err != nil || leaked {
				t.Fatalf("audit invitation disclosure=%v: %v", leaked, err)
			}
			if strings.Contains(requestBody(t, router, cookie, "GET", fmt.Sprintf("/users/%d", u.ID), 200), string(match[1])) {
				t.Fatal("Administrator page exposed invitation secret")
			}
		})
	}
}

func TestInvitationInitialIssuanceCannotUndoNewerDecisionHTTP(t *testing.T) {
	for _, decision := range []string{"disable", "email", "resend"} {
		t.Run(decision, func(t *testing.T) {
			pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.test'`, listener.Addr().(*net.TCPAddr).Port); err != nil {
				t.Fatal(err)
			}
			message := make(chan []byte, 2)
			go func() {
				count := 1
				if decision == "resend" {
					count = 2
				}
				for range count {
					b, _ := receiveTestSMTP(listener)
					message <- b
				}
			}()
			// Pause the real creation request at the recorded user-created activity,
			// after CreateAccount returned but before the old separate token command.
			if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_created_activity() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='user_created' THEN PERFORM pg_advisory_xact_lock(210021); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_created_activity BEFORE INSERT ON activity_logs FOR EACH ROW EXECUTE FUNCTION pause_created_activity()`); err != nil {
				t.Fatal(err)
			}
			fence, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Rollback(ctx)
			if _, err := fence.Exec(ctx, `SELECT pg_advisory_xact_lock(210021)`); err != nil {
				t.Fatal(err)
			}
			var pid int
			if err := fence.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, admin)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- onboardingPost(router, cookie, "/users", url.Values{"name": {"Initial issuance"}, "email": {"initial21@example.test"}, "role_id": {"3"}, "send_welcome_email": {"on"}})
			}()
			waitOnboardingFence(t, pool, pid)
			u := client.User.Query().Where(user.EmailEQ("initial21@example.test")).OnlyX(ctx)
			var replacement string
			switch decision {
			case "disable":
				accountPost(t, router, cookie, fmt.Sprintf("/users/%d/disable", u.ID), nil, 303)
			case "email":
				accountPost(t, router, cookie, fmt.Sprintf("/users/%d", u.ID), url.Values{"name": {u.Name}, "email": {"new-identity21@example.test"}}, 303)
			case "resend":
				accountPost(t, router, cookie, fmt.Sprintf("/users/%d/resend-welcome", u.ID), nil, 303)
				replacement = invitationTokenFromMail(t, <-message)
			}
			if err := fence.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if w := <-done; w.Code != 303 {
				t.Fatalf("creation outcome %d: %s", w.Code, w.Body.String())
			}
			var b []byte
			select {
			case b = <-message:
			case <-time.After(10 * time.Second):
				t.Fatal("missing SMTP handoff")
			}
			token := invitationTokenFromMail(t, b)
			if w := onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}}); w.Code != 400 {
				t.Fatalf("stale initial request issued a usable invitation after newer %s: %d", decision, w.Code)
			}
			if replacement != "" {
				if _, err := services.NewInvitationService(client, pool).ValidateInvite(ctx, replacement); err != nil {
					t.Fatal("initial request invalidated explicit replacement", err)
				}
			}
		})
	}
}

func invitationTokenFromMail(t *testing.T, b []byte) string {
	t.Helper()
	match := regexp.MustCompile(`token=([a-f0-9]{64})`).FindSubmatch(b)
	if len(match) != 2 {
		t.Fatal("missing SMTP invitation link")
	}
	return string(match[1])
}

func TestInvitationIssuanceFailureIsAtomicHTTP(t *testing.T) {
	for _, resend := range []bool{false, true} {
		for _, failure := range []string{"token", "audit"} {
			t.Run(fmt.Sprintf("resend=%v/%s", resend, failure), func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=1,smtp_from='sender@example.test'`); err != nil {
					t.Fatal(err)
				}
				path := "/users"
				var old string
				values := url.Values{"name": {"Atomic issue"}, "email": {"atomic21@example.test"}, "role_id": {"3"}, "send_welcome_email": {"on"}}
				if resend {
					u, token, _ := pendingInvitationHTTP(t, pool, client, company, admin, "atomic21")
					old = token
					path = fmt.Sprintf("/users/%d/resend-welcome", u.ID)
					values = nil
				}
				table := "invitation_tokens"
				if failure == "audit" {
					table = "authorization_audit"
				}
				if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_issue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected issuance failure'; END $$; CREATE TRIGGER reject_issue BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION reject_issue()`); err != nil {
					t.Fatal(err)
				}
				w := onboardingPost(router, sessionCookie(t, ctx, sessions, admin), path, values)
				if w.Code != 500 {
					t.Fatalf("issuance failure: %d %s", w.Code, w.Body.String())
				}
				if !resend {
					if client.User.Query().Where(user.EmailEQ("atomic21@example.test")).ExistX(ctx) {
						t.Fatal("failed first issuance left partial account")
					}
					var tokens, audits int
					if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invitation_tokens),(SELECT count(*) FROM authorization_audit WHERE action='user.invite')`).Scan(&tokens, &audits); err != nil || tokens != 0 || audits != 0 {
						t.Fatalf("partial token/audit %d/%d: %v", tokens, audits, err)
					}
				} else if _, err := services.NewInvitationService(client, pool).ValidateInvite(ctx, old); err != nil {
					t.Fatal("failed resend consumed previous invitation", err)
				}
			})
		}
	}
}

func TestInvitationIssuanceRechecksAuthorityAfterWaitHTTP(t *testing.T) {
	for _, resend := range []bool{false, true} {
		for _, loss := range []string{"role", "session"} {
			t.Run(fmt.Sprintf("resend=%v/%s", resend, loss), func(t *testing.T) {
				pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				client.User.Create().SetCompanyID(company).SetName("Replacement Administrator").SetEmail("replacement21@example.test").SetPasswordHash(client.User.GetX(ctx, admin).PasswordHash).SetRole("tech").SetAuthorizationRoleID(1).SaveX(ctx)
				if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=1,smtp_from='sender@example.test'`); err != nil {
					t.Fatal(err)
				}
				path := "/users"
				var old string
				values := url.Values{"name": {"Authority issue"}, "email": {"authority21@example.test"}, "role_id": {"3"}, "send_welcome_email": {"on"}}
				if resend {
					u, token, _ := pendingInvitationHTTP(t, pool, client, company, admin, "authority21")
					old = token
					path = fmt.Sprintf("/users/%d/resend-welcome", u.ID)
					values = nil
				}
				cookie := sessionCookie(t, ctx, sessions, admin)
				fence, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer fence.Rollback(ctx)
				if _, err := fence.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
					t.Fatal(err)
				}
				var pid int
				if err := fence.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					t.Fatal(err)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- onboardingPost(router, cookie, path, values) }()
				waitOnboardingFence(t, pool, pid)
				query := `DELETE FROM sessions WHERE user_id=$1`
				if loss == "role" {
					query = `UPDATE users SET authorization_role_id=3 WHERE id=$1`
				}
				if _, err := fence.Exec(ctx, query, admin); err != nil {
					t.Fatal(err)
				}
				if err := fence.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if w := <-done; w.Code != 403 {
					t.Fatalf("authority loss response %d: %s", w.Code, w.Body.String())
				}
				if !resend {
					if client.User.Query().Where(user.EmailEQ("authority21@example.test")).ExistX(ctx) {
						t.Fatal("revoked actor created account")
					}
				} else if _, err := services.NewInvitationService(client, pool).ValidateInvite(ctx, old); err != nil {
					t.Fatal("revoked actor replaced invitation", err)
				}
			})
		}
	}
}

func TestInvitationResendAndRedemptionSerializeHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	u, token, _ := pendingInvitationHTTP(t, pool, client, company, admin, "resend-race21")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.test'`, listener.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatal(err)
	}
	mail := make(chan []byte, 1)
	go func() { body, _ := receiveTestSMTP(listener); mail <- body }()
	cookie := sessionCookie(t, ctx, sessions, admin)
	fence, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(ctx)
	if _, err := fence.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := fence.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	accept, resend := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	go func() {
		accept <- onboardingPost(router, nil, "/accept-invite", url.Values{"token": {token}, "password": {"ChosenPassword1!"}, "confirm_password": {"ChosenPassword1!"}})
	}()
	go func() { resend <- onboardingPost(router, cookie, fmt.Sprintf("/users/%d/resend-welcome", u.ID), nil) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE $1::integer=ANY(pg_blocking_pids(pid))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("concurrent requests did not both reach fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := fence.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	a, b := <-accept, <-resend
	if !(a.Code == 303 && b.Code == 409 || a.Code == 400 && b.Code == 303) {
		t.Fatalf("resend/redemption not serialized: accept=%d resend=%d", a.Code, b.Code)
	}
	if a.Code == 303 {
		if !client.User.GetX(ctx, u.ID).IsActive {
			t.Fatal("accepted user not active")
		}
	} else {
		if client.User.GetX(ctx, u.ID).IsActive {
			t.Fatal("resend activated account")
		}
		var message []byte
		select {
		case message = <-mail:
		case <-time.After(10 * time.Second):
			t.Fatal("replacement mail missing")
		}
		if _, err := services.NewInvitationService(client, pool).ValidateInvite(ctx, invitationTokenFromMail(t, message)); err != nil {
			t.Fatal("winning replacement not usable", err)
		}
	}
}
