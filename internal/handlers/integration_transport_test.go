package handlers

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/credentials"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
)

func TestQueuedSMTPUsesCurrentTransportAndFrozenSender(t *testing.T) {
	pool, client, _, _, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='old.invalid',smtp_from='original@example.com'`)
	if err != nil {
		t.Fatal(err)
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Transport customer").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Frozen estimate").SaveX(ctx)
	svc := delivery.New(pool, "")
	d, err := svc.Queue(ctx, delivery.Actor{ID: admin, CompanyID: company, Role: "admin"}, delivery.QueueRequest{Key: uuid.New(), Document: delivery.DocumentRef{Type: "estimate", ID: doc.ID}, Snapshot: delivery.Snapshot{To: []string{"original-recipient@example.com"}, Subject: "Original subject", TextBody: "Original text", HTMLBody: "<p>Original HTML</p>", PDF: []byte("frozen attachment"), PDFFilename: "original.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	message := make(chan string, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		p := textproto.NewConn(conn)
		_ = p.PrintfLine("220 test smtp")
		var envelope string
		for {
			line, e := p.ReadLine()
			if e != nil {
				return
			}
			if strings.HasPrefix(line, "MAIL FROM:") || strings.HasPrefix(line, "RCPT TO:") {
				envelope += line + "\n"
			}
			if line == "DATA" {
				_ = p.PrintfLine("354 send")
				data, _ := io.ReadAll(p.DotReader())
				message <- envelope + string(data)
				_ = p.PrintfLine("250 accepted")
			} else {
				_ = p.PrintfLine("250 ok")
			}
		}
	}()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='replacement@example.com',estimate_email_body='Replacement body',estimate_email_html='<p>Replacement HTML</p>'`, listener.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatal(err)
	}
	processed, err := svc.ProcessOne(ctx, delivery.NewSMTPSender(services.NewEmailService(services.NewCompanySettingsService(client))), delivery.NopAcceptanceHook{})
	if err != nil || !processed {
		t.Fatalf("worker: %v %v", processed, err)
	}
	select {
	case msg := <-message:
		for _, want := range []string{"MAIL FROM:<original@example.com>", "RCPT TO:<original-recipient@example.com>", "From: original@example.com", "Message-ID: " + d.MessageID, "Original text", "Original HTML", "original.pdf", "ZnJvemVuIGF0dGFjaG1lbnQ="} {
			if !strings.Contains(msg, want) {
				t.Fatalf("frozen MIME missing %q: %s", want, msg)
			}
		}
		if strings.Contains(msg, "replacement") {
			t.Fatal("transport settings rewrote content")
		}
	case <-time.After(time.Second):
		t.Fatal("new transport not used")
	}
	history, err := svc.History(ctx, company, delivery.DocumentRef{Type: "estimate", ID: doc.ID})
	if err != nil || len(history) != 1 || history[0].State != "accepted" || history[0].DeliveredAt != nil {
		t.Fatalf("SMTP fabricated delivery evidence: %+v %v", history, err)
	}
}

func TestProviderFailureCannotDiscloseStoredCredentialHTTP(t *testing.T) {
	pool, _, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	t.Setenv("FREEFSM_CREDENTIAL_KEYS", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	encrypted, err := credentials.Seal("smtp-provider-secret-canary")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		_, _ = io.WriteString(conn, "550 smtp-provider-secret-canary provider diagnostic\r\n")
	}()
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.com',smtp_password=$2`, listener.Addr().(*net.TCPAddr).Port, encrypted); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/settings/test-email", strings.NewReader(url.Values{"recipient": {"recipient@example.com"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(sessionCookie(t, ctx, sessions, admin))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 503 || strings.Contains(w.Body.String(), "smtp-provider-secret-canary") || strings.Contains(w.Body.String(), "accepted the test") {
		t.Fatalf("unsafe provider error: %d %s", w.Code, w.Body.String())
	}
	var leaked bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE detail::text LIKE '%smtp-provider-secret-canary%')`).Scan(&leaked); err != nil || leaked {
		t.Fatalf("provider diagnostic leaked into audit: %v", err)
	}
}
