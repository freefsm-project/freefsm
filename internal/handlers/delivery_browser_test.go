package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestDeliveryPersistedChromiumAttachmentsAndLocalSMTP(t *testing.T) {
	if os.Getenv("BROWSER_TESTS") != "1" {
		t.Skip("set BROWSER_TESTS=1 and FREEFSM_TEST_DATABASE_URL")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Browser customer").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetTitle("Browser Delivery").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='reviewed@example.test',smtp_host='localhost',email_auto_cc='default@example.test' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
	original := requestBody(t, web, cookie, "GET", path+"/pdf?version="+version, 200)
	disk := filepath.Join(t.TempDir(), "extra.txt")
	historyCheck(t, os.WriteFile(disk, []byte("browser exact File bytes"), 0600))
	var fileID int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by) VALUES($1,'estimate',$2,'reviewed.txt','extra.txt','text/plain',24,$3,$4) RETURNING id`, company, doc.ID, disk, admin).Scan(&fileID))
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("../../cmd/freefsm/static"))))
	mux.Handle("/", middleware.BrowserCSRF(middleware.CSRFToken(web)))
	server := httptest.NewServer(mux)
	defer server.Close()
	bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	output, err := exec.CommandContext(bounded, "node", "testdata/browser/delivery-compose.cjs", server.URL, cookie.Value, fmt.Sprint(doc.ID), version, fmt.Sprint(fileID)).CombinedOutput()
	t.Log(string(output))
	historyCheck(t, err)
	history, err := delivery.New(pool, "").History(ctx, company, delivery.DocumentRef{Type: "estimate", ID: doc.ID})
	historyCheck(t, err)
	if len(history) != 2 {
		t.Fatalf("browser fanout=%+v", history)
	}
	historyCheck(t, os.WriteFile(disk, []byte("later changed bytes"), 0600))
	_, err = pool.Exec(ctx, `UPDATE files SET original_name='later.txt' WHERE id=$1`, fileID)
	historyCheck(t, err)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	historyCheck(t, err)
	defer listener.Close()
	messages := make(chan []byte, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 2 {
			conn, e := listener.Accept()
			if e != nil {
				return
			}
			func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
				p := textproto.NewConn(conn)
				_ = p.PrintfLine("220 owned test SMTP")
				for {
					line, e := p.ReadLine()
					if e != nil {
						return
					}
					if line == "QUIT" {
						_ = p.PrintfLine("221 bye")
						return
					}
					if line == "DATA" {
						_ = p.PrintfLine("354 data")
						data, e := io.ReadAll(p.DotReader())
						if e != nil {
							return
						}
						messages <- data
						_ = p.PrintfLine("250 accepted")
					} else {
						_ = p.PrintfLine("250 ok")
					}
				}
			}()
		}
	}()
	_, err = pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='changed@example.test' WHERE company_id=$2`, listener.Addr().(*net.TCPAddr).Port, company)
	historyCheck(t, err)
	worker := delivery.New(pool, "")
	sender := delivery.NewSMTPSender(services.NewEmailService(services.NewCompanySettingsService(client)))
	for range 2 {
		worked, err := worker.ProcessOne(ctx, sender, nil)
		historyCheck(t, err)
		if !worked {
			t.Fatal("no queued Delivery")
		}
		select {
		case raw := <-messages:
			message, err := mail.ReadMessage(bytes.NewReader(raw))
			historyCheck(t, err)
			if message.Header.Get("From") != "reviewed@example.test" || message.Header.Get("Cc") != "" || strings.Contains(string(raw), "default@example.test") {
				t.Fatalf("changed sender or retained default: %s", raw)
			}
			_, params, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
			historyCheck(t, err)
			parts := multipart.NewReader(message.Body, params["boundary"])
			attachments := map[string][]byte{}
			for {
				part, err := parts.NextPart()
				if err == io.EOF {
					break
				}
				historyCheck(t, err)
				if part.FileName() == "" {
					continue
				}
				data, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
				historyCheck(t, err)
				attachments[part.FileName()] = data
			}
			if string(attachments["reviewed.txt"]) != "browser exact File bytes" || len(attachments) != 2 {
				t.Fatalf("SMTP extras=%v", attachments)
			}
			pdfFound := false
			for name, data := range attachments {
				if name != "reviewed.txt" && string(data) == original {
					pdfFound = true
				}
			}
			if !pdfFound {
				t.Fatal("SMTP did not retain exact selected PDF")
			}
		case <-time.After(10 * time.Second):
			t.Fatal("local SMTP capture timed out")
		}
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("owned SMTP did not stop")
	}
}
