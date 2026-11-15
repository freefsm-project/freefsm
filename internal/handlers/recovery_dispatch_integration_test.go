package handlers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
)

type delayedRecoverySMTP struct {
	net.Listener
	entered, release chan struct{}
}

func (l delayedRecoverySMTP) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e == nil {
		close(l.entered)
		<-l.release
	}
	return c, e
}

func TestRecoveryDurableDispatchDoesNotEnumerateWithDelayedSMTP(t *testing.T) {
	f := permissionHTTPControlledMailInstanceFixture(t)
	pool, client, router, company, admin := f.pool, f.client, f.web, f.company, f.admin
	ctx := context.Background()
	for _, name := range []string{"disabled", "pending"} {
		client.User.Create().SetCompanyID(company).SetName(name).SetEmail(name + "@test").SetRole("tech").SetPasswordHash("unchanged").SetIsActive(false).SaveX(ctx)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client.CompanySettings.Update().SetSMTPHost("127.0.0.1").SetSMTPPort(listener.Addr().(*net.TCPAddr).Port).SetSMTPFrom("sender@example.test").SaveX(ctx)
	requestNumber := 0
	post := func(address string) *httptest.ResponseRecorder {
		requestNumber++
		r := httptest.NewRequest("POST", "/forgot-password", strings.NewReader(url.Values{"email": {address}}.Encode()))
		r.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", requestNumber)
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		return w
	}
	var body string
	for _, address := range []string{"admin@test", "unknown@test", "disabled@test", "pending@test"} {
		w := post(address)
		if w.Code != 200 {
			t.Fatalf("%s status %d %s", address, w.Code, w.Body.String())
		}
		if body != "" && w.Body.String() != body {
			t.Fatal("public response enumerates account")
		}
		body = w.Body.String()
	}
	var queued int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM password_recovery_requests WHERE completed_at IS NULL`).Scan(&queued); err != nil || queued != 4 {
		t.Fatalf("durable requests=%d %v", queued, err)
	}
	// Reconstruct the dispatcher after HTTP acknowledgement: persisted jobs survive
	// service restart, and a stalled provider cannot delay any public response.
	resets := services.NewPasswordResetService(client, pool)
	email := f.email
	delayed := delayedRecoverySMTP{listener, make(chan struct{}), make(chan struct{})}
	allowSMTP := sync.OnceFunc(func() { close(delayed.release) })
	defer allowSMTP()
	messages := make(chan []byte, 1)
	smtpErr := make(chan error, 1)
	go func() { b, e := receiveTestSMTP(delayed); messages <- b; smtpErr <- e }()
	dispatched := make(chan error, 1)
	go func() { _, e := resets.DispatchOne(ctx, email); dispatched <- e }()
	select {
	case <-delayed.entered:
	case err := <-dispatched:
		t.Fatalf("dispatch exited before SMTP admission: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP not reached")
	}
	for _, address := range []string{"admin@test", "unknown@test", "disabled@test", "pending@test"} {
		response := make(chan *httptest.ResponseRecorder, 1)
		go func() { response <- post(address) }()
		select {
		case w := <-response:
			if w.Code != 200 || w.Body.String() != body {
				t.Fatal("delayed SMTP leaked account")
			}
		case <-time.After(2 * time.Second):
			allowSMTP()
			t.Fatal("public response waited for account SMTP")
		}
	}
	if err = authorization.New(pool).EditIdentity(ctx, authorization.Actor{UserID: admin}, admin, "Admin", "new-admin@test"); err != nil {
		t.Fatal(err)
	}
	allowSMTP()
	if err = <-dispatched; err != nil {
		t.Fatal(err)
	}
	message := <-messages
	if err = <-smtpErr; err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`token=([a-f0-9]{64})`).FindSubmatch(message)
	if len(match) != 2 {
		t.Fatalf("no recovery proof in SMTP message: %s", message)
	}
	token := string(match[1])
	if _, err = resets.Validate(ctx, token); err == nil {
		t.Fatal("in-flight old-mailbox proof remained valid")
	}
	// After delivery, identity replacement invalidates both the delivered proof and
	// every still-queued request tied to the old identity revision.
	if w := recoveryPost(router, token, "MailboxAttack1!"); w.Code == 303 {
		t.Fatal("old mailbox proof survived identity replacement")
	}
	for i := 0; i < 7; i++ {
		if worked, e := resets.DispatchOne(ctx, email); e != nil || !worked {
			t.Fatalf("drain %d: %v %v", i, worked, e)
		}
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM password_reset_tokens WHERE purpose='password-recovery-v1'`).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("stale queued intent issued proof: %d %v", queued, err)
	}
}

func TestRecoveryFailedSMTPIsGenericAndQueueResumes(t *testing.T) {
	f := permissionHTTPControlledMailInstanceFixture(t)
	pool, client, router := f.pool, f.client, f.web
	ctx := context.Background()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client.CompanySettings.Update().SetSMTPHost("127.0.0.1").SetSMTPPort(listener.Addr().(*net.TCPAddr).Port).SetSMTPFrom("sender@example.test").SaveX(ctx)
	resets := services.NewPasswordResetService(client, pool)
	email := f.email
	for _, address := range []string{"admin@test", "unknown@test"} {
		r := httptest.NewRequest("POST", "/forgot-password", strings.NewReader(url.Values{"email": {address}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("request=%d", w.Code)
		}
	}
	// Simulate an abandoned worker's durable claim, then resume with a new worker.
	if _, err = pool.Exec(ctx, `UPDATE password_recovery_requests SET claimed_until=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	failed := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e == nil {
			_, e = io.WriteString(c, "421 transport unavailable\r\n")
			c.Close()
		}
		failed <- e
	}()
	for i := 0; i < 2; i++ {
		if worked, e := services.NewPasswordResetService(client, pool).DispatchOne(ctx, email); e != nil || !worked {
			t.Fatalf("dispatch: %v %v", worked, e)
		}
	}
	if err = <-failed; err != nil {
		t.Fatal(err)
	}
	var n int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM password_recovery_requests WHERE completed_at IS NOT NULL AND email=''`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("completed=%d %v", n, err)
	}
	// Neither HTTP path had to know whether an account or a transport failure existed.
	t.Log(fmt.Sprintf("%d generic durable requests completed after SMTP rejection", n))
	_ = resets
}
