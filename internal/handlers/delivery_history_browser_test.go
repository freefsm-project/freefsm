package handlers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestDeliveryPersistedChromiumRetryHistoryLocalSMTP(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("History browser").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("History browser").SaveX(ctx)
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	historyCheck(t, err)
	done := make(chan struct{})
	messages := make(chan string, 1)
	t.Cleanup(func() { listener.Close(); <-done })
	go func() {
		defer close(done)
		for attempt := 0; attempt < 3; attempt++ {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				p := textproto.NewConn(conn)
				_ = p.PrintfLine("220 owned retry SMTP")
				for {
					line, e := p.ReadLine()
					if e != nil {
						return
					}
					switch {
					case line == "QUIT":
						_ = p.PrintfLine("221 bye")
						return
					case strings.HasPrefix(line, "RCPT") && attempt == 0:
						_ = p.PrintfLine("451 owned temporary rejection")
					case strings.HasPrefix(line, "RCPT") && attempt == 1:
						_ = p.PrintfLine("550 owned permanent rejection")
					case line == "DATA":
						_ = p.PrintfLine("354 data")
						data, e := io.ReadAll(p.DotReader())
						if e != nil {
							return
						}
						messages <- string(data)
						_ = p.PrintfLine("250 accepted")
					default:
						_ = p.PrintfLine("250 ok")
					}
				}
			}()
		}
	}()
	_, err = pool.Exec(ctx, `UPDATE company_settings SET smtp_from='frozen@example.test',smtp_host='127.0.0.1',smtp_port=$1 WHERE company_id=$2`, listener.Addr().(*net.TCPAddr).Port, company)
	historyCheck(t, err)
	expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
	fieldPost(t, web, cookie, path+"/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"recipient@example.test"}, "subject": {"Frozen retry"}, "body": {"Frozen body"}}, 303)
	var id int64
	var messageID string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id,message_id FROM document_deliveries`).Scan(&id, &messageID))
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	browser := func(state, attempts, next, reason, action string) {
		t.Helper()
		bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		out, err := exec.CommandContext(bounded, "node", "testdata/browser/delivery-history.cjs", server.URL, cookie.Value, fmt.Sprint(doc.ID), state, attempts, next, reason, action).CombinedOutput()
		t.Log(string(out))
		historyCheck(t, err)
	}
	sender := delivery.NewSMTPSender(services.NewEmailService(services.NewCompanySettingsService(client)))
	process := func() {
		t.Helper()
		worked, err := delivery.New(pool, "").ProcessOne(ctx, sender, nil)
		historyCheck(t, err)
		if !worked {
			t.Fatal("missing queued intent")
		}
	}
	process()
	browser("queued", "1", "yes", "451", "")
	_, err = pool.Exec(ctx, `UPDATE document_deliveries SET next_attempt_at=clock_timestamp() WHERE id=$1`, id)
	historyCheck(t, err)
	process()
	browser("failed", "2", "no", "550", "retry-lost-response")
	process()
	browser("accepted", "3", "no", "550", "")
	select {
	case raw := <-messages:
		if !strings.Contains(raw, messageID) || !strings.Contains(raw, "Frozen retry") {
			t.Fatal("SMTP changed message identity/content")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no SMTP capture")
	}
	var intents, retries int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries`).Scan(&intents))
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_delivery_events WHERE evidence->>'manual'='true'`).Scan(&retries))
	if intents != 1 || retries != 1 {
		t.Fatalf("lost response duplicated intent/retry: %d/%d", intents, retries)
	}
}
