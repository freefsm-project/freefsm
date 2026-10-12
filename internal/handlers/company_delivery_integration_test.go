package handlers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
)

func TestCompanyConfigurationEmailPreservesSelectedPDFHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Selected PDF customer").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetTitle("Selected PDF invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	path := fmt.Sprintf("/invoices/%d", i.ID)
	expectStatus(t, router, adminCookie, "GET", path+"/email", 409)
	expectStatus(t, router, adminCookie, "POST", path+"/pdf/save", 303)
	preview := requestBody(t, router, adminCookie, "GET", path+"/pdf/preview", 200)
	version := regexp.MustCompile(`name="version" value="([0-9]+)"`).FindStringSubmatch(preview)[1]
	original := requestBody(t, router, adminCookie, "GET", path+"/pdf?version="+version, 200)
	svc := authorization.New(pool)
	role, err := svc.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Delivery without generation", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.delivery.request", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("sender@test").SetName("Sender").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := svc.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	compose := requestBody(t, router, cookie, "GET", path+"/email?version="+version, 200)
	assertContains(t, compose, `name="pdf_version" value="`+version+`"`)
	expectStatus(t, router, cookie, "POST", path+"/pdf/save", 403)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	mailbox := make(chan []byte, 1)
	smtpErr := make(chan error, 1)
	go func() {
		message, err := receiveTestSMTP(listener)
		if err != nil {
			smtpErr <- err
			return
		}
		mailbox <- message
	}()
	cs := client.CompanySettings.Query().OnlyX(ctx)
	client.CompanySettings.UpdateOne(cs).SetSMTPHost("127.0.0.1").SetSMTPPort(listener.Addr().(*net.TCPAddr).Port).SetSMTPFrom("sender@example.test").SetInvoiceLogoPath("/nonexistent/selected-pdf-must-not-regenerate.png").SaveX(ctx)
	values := url.Values{"pdf_version": {version}, "idempotency_key": {uuid.NewString()}, "to": {"recipient@example.test"}, "subject": {"Keep selected PDF"}, "body": {"Entered message stays unchanged"}}
	post := func(want int) string {
		t.Helper()
		r := httptest.NewRequest("POST", path+"/email", strings.NewReader(values.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("email status=%d want=%d %s", w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	stale := post(409)
	assertContains(t, stale, "stale")
	assertContains(t, stale, "Entered message stays unchanged")
	assertContains(t, stale, `name="pdf_version" value="`+version+`"`)
	values.Set("confirm_stale_pdf", "1")
	post(303)
	worker := delivery.New(pool, "")
	worked, err := worker.ProcessOne(ctx, delivery.NewSMTPSender(services.NewEmailService(services.NewCompanySettingsService(client))), delivery.NopAcceptanceHook{})
	if err != nil || !worked {
		t.Fatalf("delivery worked=%v err=%v", worked, err)
	}
	var raw []byte
	select {
	case raw = <-mailbox:
	case err := <-smtpErr:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("no SMTP message")
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	parts := multipart.NewReader(message.Body, params["boundary"])
	found := false
	for {
		part, err := parts.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasPrefix(part.Header.Get("Content-Type"), "application/pdf") {
			data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, []byte(original)) {
				t.Fatal("SMTP sent substituted PDF bytes")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("SMTP PDF attachment missing")
	}
	post(303) // Same accepted intent does not create another outbox effect.
	worked, err = worker.ProcessOne(ctx, delivery.NewSMTPSender(services.NewEmailService(services.NewCompanySettingsService(client))), delivery.NopAcceptanceHook{})
	if err != nil || worked {
		t.Fatalf("replay produced another delivery: %v %v", worked, err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_pdf_delivery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='invoice.delivery.request' THEN RAISE EXCEPTION 'injected delivery audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_pdf_delivery_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_pdf_delivery_audit()`); err != nil {
		t.Fatal(err)
	}
	values.Set("idempotency_key", uuid.NewString())
	post(500)
	worked, err = worker.ProcessOne(ctx, delivery.NewSMTPSender(services.NewEmailService(services.NewCompanySettingsService(client))), delivery.NopAcceptanceHook{})
	if err != nil || worked {
		t.Fatalf("audit failure published an outbox effect: %v %v", worked, err)
	}
}

func receiveTestSMTP(listener net.Listener) ([]byte, error) {
	conn, err := listener.Accept()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return nil, err
	}
	reader := textproto.NewReader(bufio.NewReader(conn))
	reply := func(text string) error { _, err := io.WriteString(conn, text+"\r\n"); return err }
	if err := reply("220 localhost SMTP"); err != nil {
		return nil, err
	}
	var message []byte
	for {
		line, err := reader.ReadLine()
		if err != nil {
			if err == io.EOF && len(message) > 0 {
				return message, nil
			}
			return nil, err
		}
		switch {
		case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"), strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
			err = reply("250 OK")
		case line == "DATA":
			if err = reply("354 Send message"); err != nil {
				return nil, err
			}
			message, err = reader.ReadDotBytes()
			if err != nil {
				return nil, err
			}
			err = reply("250 Accepted")
		case line == "QUIT":
			err = reply("221 Bye")
			return message, err
		default:
			err = reply("500 Unsupported")
		}
		if err != nil {
			return nil, err
		}
	}
}
