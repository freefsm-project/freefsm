package handlers

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/ent/user"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/services"
)

// Run with the owned namespace fixture: literal ports select production TLS
// modes, and SSL_CERT_FILE must be set before this process initializes roots.
func TestEmailAllPathPolicyDenialHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPInstanceFixture(t)
	ctx := context.Background()
	control, err := instancecontrol.New(t.TempDir())
	historyCheck(t, err)
	manager := admissionManager(t, pool, control)
	startup, err := manager.BeginStartup(ctx)
	historyCheck(t, err)
	defer startup.Close()
	importFixtureEmailPolicy(t, pool, control, manager)
	historyCheck(t, startup.Finish(ctx))
	cookie := sessionCookie(t, ctx, sessions, admin)
	pending, _, _ := pendingInvitationHTTP(t, pool, client, company, admin, "all-path-pending")
	email := services.NewControlledEmailService(services.NewCompanySettingsService(client), control)
	resets := services.NewPasswordResetService(client, pool)
	_, err = resets.CreateToken(ctx, admin, "admin@test")
	historyCheck(t, err)
	historyCheck(t, resets.Request(ctx, "admin@test", "http://localhost/reset-password"))
	to := services.EmailRecipients{To: []string{"recipient@example.test"}}
	attachment := services.EmailAttachment{Filename: "snapshot.pdf", ContentType: "application/pdf", Data: []byte("%PDF-1.4\nfixture")}
	wrappers := map[string]func() error{
		"welcome": func() error {
			return email.SendWelcomeEmail(ctx, to.To[0], "Recipient", "http://localhost/accept-invite")
		},
		"recovery": func() error {
			return email.SendPasswordReset(ctx, to.To[0], "Recipient", "http://localhost/reset-password")
		},
		"test":     func() error { return email.SendTestEmail(ctx, to.To[0], "Recipient") },
		"email":    func() error { return email.SendEmail(ctx, to.To[0], "Subject", "Body") },
		"email-to": func() error { return email.SendEmailTo(ctx, to, "Subject", "Body") },
		"attachment": func() error {
			return email.SendEmailWithAttachment(ctx, to.To[0], "Subject", "Body", attachment.Filename, attachment.ContentType, attachment.Data)
		},
		"attachment-to": func() error {
			return email.SendEmailWithAttachmentTo(ctx, to, "Subject", "Body", attachment.Filename, attachment.ContentType, attachment.Data)
		},
		"document": func() error {
			return email.SendDocumentSnapshot(ctx, to, "Subject", "Body", "<p>Body</p>", "<all-path@example.test>", attachment)
		},
		"company-document": func() error {
			return email.SendCompanyDocumentSnapshot(ctx, company, to, "Subject", "Body", "<p>Body</p>", "<all-path@example.test>", attachment)
		},
		"company-attachments": func() error {
			return email.SendCompanyDocumentAttachments(ctx, company, to, "Subject", "Body", "<p>Body</p>", "<all-path@example.test>", "sender@example.test", []services.EmailAttachment{attachment})
		},
	}
	for _, port := range []int{0, 587, 465} {
		t.Run(fmt.Sprintf("port-%d", port), func(t *testing.T) {
			// Each transport is an independent HTTP fixture, including its
			// per-IP and per-login budgets; all share the real policy owner.
			web := control.Middleware(New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()}, control))
			listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: port})
			historyCheck(t, err)
			defer listener.Close()
			t.Logf("owned bind succeeded: %s", listener.Addr())
			_, err = pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.test'`, listener.Addr().(*net.TCPAddr).Port)
			historyCheck(t, err)
			for _, state := range []string{"disabled", "unavailable"} {
				t.Run(state, func(t *testing.T) {
					// Keep the current epoch policy and explicitly remove only its import
					// confirmation for the unavailable fault; never import it again.
					_, err := pool.Exec(ctx, `UPDATE restore_email_policy SET enabled=false`)
					historyCheck(t, err)
					_, err = pool.Exec(ctx, `UPDATE restore_email_policy_import SET complete=$1`, state == "disabled")
					historyCheck(t, err)
					want := instancecontrol.ErrEmailDisabled
					if state == "unavailable" {
						want = instancecontrol.ErrEmailUnavailable
					}
					before := historyDurableRows(t, pool, []string{"invitation_tokens", "password_reset_tokens", "password_recovery_requests"})
					for name, send := range wrappers {
						t.Run(name, func(t *testing.T) {
							if err := send(); !errors.Is(err, want) {
								t.Fatalf("gate=%v want=%v", err, want)
							}
						})
					}
					address := fmt.Sprintf("blocked-%d-%s@example.test", port, state)
					created := onboardingPost(web, cookie, "/users", url.Values{"name": {"Blocked"}, "email": {address}, "role_id": {"3"}, "send_welcome_email": {"on"}})
					location, _ := url.QueryUnescape(created.Header().Get("Location"))
					if created.Code != 303 || !strings.Contains(strings.ToLower(location), "manual") || client.User.Query().Where(user.EmailEQ(address)).ExistX(ctx) {
						t.Fatalf("invitation did not fail before account creation: %d", created.Code)
					}
					for _, route := range []string{fmt.Sprintf("/users/%d/resend-welcome", pending.ID), fmt.Sprintf("/users/%d/resend-recovery", admin)} {
						response := onboardingPost(web, cookie, route, nil)
						if response.Code != http.StatusSeeOther {
							t.Fatalf("%s: %d", route, response.Code)
						}
					}
					for _, address := range []string{"admin@test", "missing@example.test"} {
						response := onboardingPost(web, nil, "/forgot-password", url.Values{"email": {address}})
						if response.Code != http.StatusServiceUnavailable {
							t.Fatalf("public recovery: %d", response.Code)
						}
					}
					if response := onboardingPost(web, cookie, "/settings/test-email", url.Values{"recipient": {to.To[0]}}); response.Code != 503 {
						t.Fatalf("test mail: %d", response.Code)
					}
					if worked, err := resets.DispatchOne(ctx, email); worked || !errors.Is(err, want) {
						t.Fatalf("recovery worker: %v %v", worked, err)
					}
					var claimed int
					historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM password_recovery_requests WHERE claimed_until IS NOT NULL OR completed_at IS NOT NULL`).Scan(&claimed))
					if claimed != 0 {
						t.Fatal("denied recovery changed durable queue")
					}
					historyEqual(t, "denied producers preserve invitations, recovery tokens and queue", historyDurableRows(t, pool, []string{"invitation_tokens", "password_reset_tokens", "password_recovery_requests"}), before)
				})
			}
			historyCheck(t, listener.SetDeadline(time.Now().Add(30*time.Millisecond)))
			conn, err := listener.Accept()
			if conn != nil {
				conn.Close()
				t.Fatal("disabled/unavailable path reached owned transport")
			}
			if timeout, ok := err.(net.Error); !ok || !timeout.Timeout() {
				t.Fatalf("sink observation: %v", err)
			}
			t.Log("disabled and unavailable wrappers/routes: zero TCP connections")
			{
				// Repair the injected unavailable import fact, then require the real
				// sealed action before proving this same owned sink can receive mail.
				_, err := pool.Exec(ctx, `UPDATE restore_email_policy_import SET complete=true`)
				historyCheck(t, err)
				policy, err := emailpolicy.Read(ctx, pool, false)
				historyCheck(t, err)
				enabled := onboardingPost(web, cookie, "/settings/enable-email", url.Values{"save_action_id": {newOperationKey()}, "save_revision": {policy.Review()}})
				if enabled.Code != 303 || enabled.Header().Get("X-FreeFSM-Disposition") != "saved" {
					t.Fatalf("sealed enable: %d", enabled.Code)
				}
				var certificate tls.Certificate
				var certificateErr error
				if port != 0 {
					certificate, err = tls.LoadX509KeyPair(os.Getenv("FREEFSM_TLS_TEST_CERT"), os.Getenv("FREEFSM_TLS_TEST_KEY"))
					historyCheck(t, err)
					leaf, err := x509.ParseCertificate(certificate.Certificate[0])
					historyCheck(t, err)
					roots, err := x509.SystemCertPool()
					historyCheck(t, err)
					_, certificateErr = leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: "127.0.0.1"})
				}
				for name, send := range wrappers {
					t.Run("enabled-"+name, func(t *testing.T) {
						historyCheck(t, listener.SetDeadline(time.Now().Add(10*time.Second)))
						done := make(chan allPathSMTPResult, 1)
						go func() { done <- receiveAllPathSMTP(listener, port, certificate) }()
						sendErr := send()
						var result allPathSMTPResult
						select {
						case result = <-done:
						case <-time.After(12 * time.Second):
							t.Fatal("owned SMTP goroutine did not join")
						}
						negative := os.Getenv("FREEFSM_TLS_TEST_NEGATIVE")
						if port != 0 && negative != "" {
							var authority x509.UnknownAuthorityError
							var hostname x509.HostnameError
							// Production intentionally sanitizes transport errors. Check
							// trust independently, then require the actual peer alert
							// and failed production send, never just an arbitrary error.
							if sendErr == nil || (negative == "wrong-ca" && !errors.As(certificateErr, &authority)) ||
								(negative == "wrong-name" && !errors.As(certificateErr, &hostname)) ||
								(negative != "wrong-ca" && negative != "wrong-name") {
								t.Fatalf("expected %s certificate rejection, got %v", negative, sendErr)
							}
							if result.accepted || result.handshake || result.err == nil || !strings.Contains(result.err.Error(), "remote error: tls: bad certificate") {
								t.Fatalf("certificate rejection reached SMTP acceptance: %+v", result)
							}
							t.Logf("%s rejected before DATA acceptance; peer=%v; sink joined", negative, result.err)
							return
						}
						historyCheck(t, sendErr)
						historyCheck(t, certificateErr)
						historyCheck(t, result.err)
						if !result.accepted || (port != 0 && !result.handshake) {
							t.Fatalf("missing SMTP250/TLS handoff: %+v", result)
						}
						t.Logf("SMTP DATA250 accepted; TLS=%v; sink joined", result.handshake)
					})
				}
			}
		})
	}
	_, err = pool.Exec(ctx, `UPDATE restore_email_policy_import SET complete=false`)
	historyCheck(t, err)
	web := control.Middleware(New(pool, client, sessions, &config.Config{UploadDir: t.TempDir()}, control))
	manual := onboardingPost(web, cookie, "/users", url.Values{"name": {"Manual"}, "email": {"manual-allpath@example.test"}, "role_id": {"3"}, "password": {"TemporaryPassword1!"}, "confirm_password": {"TemporaryPassword1!"}})
	if manual.Code != 303 || !client.User.Query().Where(user.EmailEQ("manual-allpath@example.test")).ExistX(ctx) {
		t.Fatalf("manual unavailable onboarding: %d", manual.Code)
	}
}

type allPathSMTPResult struct {
	handshake, accepted bool
	err                 error
}

// One bounded connection per positive/negative control. The same listener was
// observed empty after every disabled/unavailable producer completed.
func receiveAllPathSMTP(listener net.Listener, port int, certificate tls.Certificate) (result allPathSMTPResult) {
	conn, err := listener.Accept()
	if err != nil {
		result.err = err
		return
	}
	defer conn.Close()
	result.err = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if result.err != nil {
		return
	}
	upgrade := func() error {
		secured := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
		if err := secured.Handshake(); err != nil {
			return err
		}
		conn = secured
		result.handshake = true
		return nil
	}
	if port == 465 {
		if result.err = upgrade(); result.err != nil {
			return
		}
	}
	reader := textproto.NewReader(bufio.NewReader(conn))
	reply := func(text string) error { _, err := io.WriteString(conn, text+"\r\n"); return err }
	if result.err = reply("220 localhost SMTP"); result.err != nil {
		return
	}
	for {
		line, err := reader.ReadLine()
		if err != nil {
			if err != io.EOF || !result.accepted {
				result.err = err
			}
			return
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
			if port == 587 && !result.handshake {
				err = reply("250-localhost\r\n250 STARTTLS")
			} else {
				err = reply("250 OK")
			}
		case line == "STARTTLS" && port == 587 && !result.handshake:
			if err = reply("220 Ready for TLS"); err == nil {
				err = upgrade()
				reader = textproto.NewReader(bufio.NewReader(conn))
			}
		case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			err = reply("250 OK")
		case line == "DATA":
			if port != 0 && !result.handshake {
				err = errors.New("DATA before TLS")
				break
			}
			if err = reply("354 Send message"); err == nil {
				var message []byte
				message, err = reader.ReadDotBytes()
				if err == nil && len(message) == 0 {
					err = errors.New("empty SMTP message")
				}
				if err == nil {
					err = reply("250 Accepted")
					result.accepted = err == nil
				}
			}
		case line == "QUIT":
			result.err = reply("221 Bye")
			return
		default:
			err = fmt.Errorf("unexpected SMTP command: %s", line)
		}
		if err != nil {
			result.err = err
			return
		}
	}
}
