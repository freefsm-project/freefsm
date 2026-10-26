package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type deliveryTestSender func(context.Context, delivery.Delivery) (delivery.SendResult, error)

func (f deliveryTestSender) Send(ctx context.Context, d delivery.Delivery) (delivery.SendResult, error) {
	return f(ctx, d)
}

func TestDeliveryEncryptedBackupDrainAndRestoreGates(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable cluster required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(context.Background())) })
	src := newHistoryBackupInstance(t, admin, dsn, "Delivery source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Delivery destination")
	customer := src.client.Customer.Create().SetCompanyID(src.company).SetDisplayName("Backup customer").SaveX(ctx)
	job := src.client.Job.Create().SetCompanyID(src.company).SetCustomerID(customer.ID).SetJobType("Backup Job").SaveX(ctx)
	doc := src.client.Estimate.Create().SetCompanyID(src.company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Frozen backup").SaveX(ctx)
	_, err = src.pool.Exec(ctx, `UPDATE company_settings SET smtp_from='original@example.test',smtp_host='localhost',estimate_email_body='Configured original' WHERE company_id=$1`, src.company)
	historyCheck(t, err)
	web := New(src.pool, src.client, src.sessions, &config.Config{UploadDir: src.uploadDir, MaxUploadSize: 25 << 20}, src.control)
	cookie := sessionCookie(t, ctx, src.sessions, src.actor)
	conversionUpload(t, web, cookie, "estimate", doc.ID, "original.txt", "frozen backup File bytes")
	var fileID int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM files`).Scan(&fileID))
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
	var version int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM document_pdf_versions`).Scan(&version))
	originalPDF := requestBody(t, web, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d", path, version), 200)
	outbox := delivery.New(src.pool, "")
	outbox.SetInstanceControl(src.control)
	api := apiv1.NewRouterWithDelivery(src.pool, src.client, src.sessions, config.APIConfig{}, nil, outbox)
	apiCall := func(router http.Handler, token, endpoint string, input any, status int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", endpoint, bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s=%d %s", endpoint, w.Code, w.Body.String())
		}
		var out map[string]any
		historyCheck(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	message := map[string]any{"pdf_version_id": version, "confirm_stale_pdf": false, "to": []string{"one@example.test", "two@example.test"}, "cc": []string{}, "bcc": []string{}, "subject": "Reviewed backup", "text_body": "Frozen body", "html_body": "<p>Frozen HTML</p>", "extra_file_ids": []int64{fileID}}
	review := apiCall(api, src.mobile, path+"/deliveries/review", message, 200)["data"].(map[string]any)
	intent := map[string]any{}
	for k, v := range message {
		intent[k] = v
	}
	intent["operation_id"] = newOperationKey()
	intent["job_id"] = job.ID
	intent["review_token"] = review["review_token"]
	receipt := apiCall(api, src.mobile, path+"/deliveries", intent, 200)["data"].(map[string]any)
	pendingID := int64(receipt["delivery_ids"].([]any)[0].(float64))
	worked, err := outbox.ProcessOne(ctx, deliveryTestSender(func(context.Context, delivery.Delivery) (delivery.SendResult, error) {
		return delivery.SendResult{}, &delivery.SendError{Kind: delivery.SendPermanent, Err: errors.New("fixture rejection")}
	}), nil)
	historyCheck(t, err)
	if !worked {
		t.Fatal("missing initial attempt")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	workerDone := make(chan error, 1)
	go func() {
		_, err := outbox.ProcessOne(ctx, deliveryTestSender(func(context.Context, delivery.Delivery) (delivery.SendResult, error) {
			close(entered)
			select {
			case <-release:
				return delivery.SendResult{Evidence: map[string]any{"transport": "owned fake acceptance"}}, nil
			case <-ctx.Done():
				return delivery.SendResult{}, ctx.Err()
			}
		}), nil)
		workerDone <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	historyCheck(t, outbox.ManualRetry(ctx, delivery.Actor{ID: src.actor, CompanyID: src.company, Role: "admin"}, pendingID, "explicit pending retry before backup", uuid.New()))
	started := src.request("create", strings.NewReader(url.Values{"archive_password": {"history-archive-secret"}}.Encode()), false, "")
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, leave, e := src.control.EnterEmail(ctx)
		if errors.Is(e, instancecontrol.ErrMaintenance) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		leave()
		if time.Now().After(deadline) {
			t.Fatal("backup did not close admission while draining active mail")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := outbox.Claim(ctx); !errors.Is(err, instancecontrol.ErrMaintenance) {
		t.Fatalf("backup claim gate=%v", err)
	}
	close(release)
	historyCheck(t, <-workerDone)
	tables := []string{"document_deliveries", "document_delivery_events", "document_pdf_versions", "delivery_operations", "operation_admissions"}
	before := historyDurableRows(t, src.pool, tables)
	op := src.operation(t, started)
	archive := src.request("download", strings.NewReader(url.Values{"operation": {op.ID}, "current_password": {"history-account-secret"}}.Encode()), false, "")
	if archive.Code != 200 || !bytes.HasPrefix(archive.Body.Bytes(), []byte("age-encryption.org/v1")) {
		t.Fatal("backup was not an authenticated encrypted archive")
	}
	op = dst.operation(t, dst.request("upload", bytes.NewReader(archive.Body.Bytes()), true, ""))
	op = dst.operation(t, dst.request("restore", strings.NewReader(url.Values{"operation": {op.ID}, "archive_digest": {op.ArchiveDigest}, "current_password": {"history-account-secret"}, "confirmation": {dst.name}}.Encode()), false, ""))
	if op.Phase != "complete" {
		t.Fatal(op)
	}
	historyCheck(t, dst.manager.RefreshConnections())
	historyEqual(t, "encrypted restore preserves snapshots IDs attempts message IDs receipts and event history", historyDurableRows(t, dst.pool, tables), before)
	var restoredName, restoredType, restoredPath string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT original_name,mime_type,file_path FROM files WHERE id=$1`, fileID).Scan(&restoredName, &restoredType, &restoredPath))
	if restoredName != "original.txt" || restoredType != "text/plain" || !strings.HasPrefix(restoredPath, dst.uploadDir+string(os.PathSeparator)) {
		t.Fatal("original File metadata/root did not restore")
	}
	fileBytes, err := os.ReadFile(restoredPath)
	historyCheck(t, err)
	if string(fileBytes) != "frozen backup File bytes" {
		t.Fatal("durable source File bytes did not restore")
	}
	reopened, err := instancecontrol.New(dst.control.StateDir())
	historyCheck(t, err)
	if !reopened.EmailDisabled() {
		t.Fatal("persistent restore mail gate absent")
	}
	restored := delivery.New(dst.pool, "")
	restored.SetInstanceControl(reopened)
	restored.SetBeforeWork(dst.manager.RefreshConnections)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	historyCheck(t, err)
	defer listener.Close()
	mailbox := make(chan []byte, 1)
	smtpDone := make(chan error, 1)
	go func() {
		raw, err := receiveTestSMTP(listener)
		if err == nil {
			mailbox <- raw
		}
		smtpDone <- err
	}()
	_, err = dst.pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='changed@example.test' WHERE company_id=$2`, listener.Addr().(*net.TCPAddr).Port, src.company)
	historyCheck(t, err)
	email := services.NewEmailService(services.NewCompanySettingsService(dst.client))
	email.SetInstanceControl(reopened)
	sender := delivery.NewSMTPSender(email)
	worked, err = restored.ProcessOne(ctx, sender, nil)
	historyCheck(t, err)
	if worked {
		t.Fatal("restored queue sent before enable")
	}
	if _, err = restored.Claim(ctx); !errors.Is(err, instancecontrol.ErrEmailDisabled) {
		t.Fatalf("restored Claim bypass: %v", err)
	}
	if err = restored.ManualRetry(ctx, delivery.Actor{}, pendingID, "blocked"); !errors.Is(err, instancecontrol.ErrEmailDisabled) {
		t.Fatalf("restored retry bypass: %v", err)
	}
	if _, err = sender.Send(ctx, delivery.Delivery{CompanyID: src.company}); !errors.Is(err, instancecontrol.ErrEmailDisabled) {
		t.Fatalf("direct transport bypass: %v", err)
	}
	api = apiv1.NewRouterWithDelivery(dst.pool, dst.client, dst.sessions, config.APIConfig{}, nil, restored)
	apiCall(api, src.mobile, path+"/deliveries", intent, 401)
	fresh, err := dst.sessions.CreateMobile(ctx, src.actor, "Restored review client")
	historyCheck(t, err)
	nextReview := apiCall(api, fresh, path+"/deliveries/review", message, 200)["data"].(map[string]any)
	intent["operation_id"] = newOperationKey()
	intent["review_token"] = nextReview["review_token"]
	apiCall(api, fresh, path+"/deliveries", intent, 503)
	newCookie := sessionCookie(t, ctx, dst.sessions, src.actor)
	web = New(dst.pool, dst.client, dst.sessions, &config.Config{UploadDir: dst.uploadDir, MaxUploadSize: 25 << 20}, reopened)
	fieldPost(t, web, newCookie, path+"/email", url.Values{"pdf_version": {fmt.Sprint(version)}, "idempotency_key": {newOperationKey()}, "to": {"new@example.test"}, "subject": {"Blocked"}, "body": {"Body"}}, 503)
	historyEqual(t, "all disabled paths preserve restored pending evidence", historyDurableRows(t, dst.pool, tables), before)
	select {
	case <-mailbox:
		t.Fatal("SMTP before Administrator enable")
	default:
	}
	r := httptest.NewRequest("POST", "/settings/enable-email", nil)
	r.Header.Set("X-CSRF-Token", dst.csrf)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.AddCookie(dst.cookie)
	r.AddCookie(newCookie)
	w := httptest.NewRecorder()
	web.ServeHTTP(w, r)
	if w.Code != 303 || reopened.EmailDisabled() {
		t.Fatalf("Admin enable=%d %s", w.Code, w.Body.String())
	}
	worked, err = restored.ProcessOne(ctx, sender, nil)
	historyCheck(t, err)
	if !worked {
		t.Fatal("eligible pending Delivery did not resume")
	}
	historyCheck(t, <-smtpDone)
	raw := <-mailbox
	mailMessage, err := mail.ReadMessage(bytes.NewReader(raw))
	historyCheck(t, err)
	var originalMessageID string
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT message_id FROM document_deliveries WHERE id=$1`, pendingID).Scan(&originalMessageID))
	if mailMessage.Header.Get("Message-ID") != originalMessageID || mailMessage.Header.Get("From") != "original@example.test" {
		t.Fatal("restored sender or message ID changed")
	}
	_, params, err := mime.ParseMediaType(mailMessage.Header.Get("Content-Type"))
	historyCheck(t, err)
	parts := multipart.NewReader(mailMessage.Body, params["boundary"])
	foundPDF, foundFile := false, false
	for {
		part, e := parts.NextPart()
		if e == io.EOF {
			break
		}
		historyCheck(t, e)
		if part.FileName() == "" {
			continue
		}
		data, e := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
		historyCheck(t, e)
		if part.FileName() == "original.txt" {
			foundFile = string(data) == "frozen backup File bytes"
		} else {
			foundPDF = string(data) == originalPDF
		}
	}
	if !foundPDF || !foundFile {
		t.Fatal("encrypted restore changed frozen attachment bytes")
	}
	var attempts, cycle int
	var state string
	var delivered *time.Time
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT lifetime_attempt_count,cycle_attempt_count,state,delivered_at FROM document_deliveries WHERE id=$1`, pendingID).Scan(&attempts, &cycle, &state, &delivered))
	if attempts != 2 || cycle != 2 || state != "accepted" || delivered != nil {
		t.Fatalf("restored history=%d/%d %s delivered=%v", attempts, cycle, state, delivered)
	}
}
