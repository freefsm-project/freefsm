package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/objectref"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"golang.org/x/crypto/bcrypt"
)

// Issue 17 acceptance: the archive must preserve usable history, not merely the
// number of current documents. ADRs 0001-0004 define the invariants exercised here.
// Like TestBackupHTTPRoundTrip, this requires an explicitly supplied disposable
// cluster; each instance owns a distinct database through a nonprivileged role.
func TestBackupHistoricalDomainRoundTrip(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable cluster required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Historical source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Disposable destination")
	if src.role == dst.role {
		t.Fatal("source and destination must use different database owners")
	}

	// Foundational company/status configuration is SQL fixture setup. Historical
	// transitions below run through application services.
	for _, typ := range []string{"estimate", "invoice"} {
		tx, err := src.pool.Begin(ctx)
		historyCheck(t, err)
		defer tx.Rollback(ctx)
		var workflow int64
		historyCheck(t, tx.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,$2,$2) RETURNING id`, src.company, typ).Scan(&workflow))
		categories := []string{"draft", "estimate", "sent", "accepted", "rejected", "completed"}
		if typ == "invoice" {
			categories = []string{"draft", "invoiced", "sent", "partially_paid", "paid", "void"}
		}
		for _, category := range categories {
			_, err := tx.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES($1,$2,$3,$4,1,true)`, src.company, workflow, category, typ+":"+category)
			historyCheck(t, err)
		}
		historyCheck(t, tx.Commit(ctx))
	}
	customer, err := services.NewCustomerService(src.client).Create(ctx, src.company, services.CustomerCreateParams{DisplayName: "Historical customer", Email: "original@example.test", Status: "customer", AccountType: "individual", CustomFields: "[]"})
	historyCheck(t, err)
	invoiceService := services.NewInvoiceService(src.client, src.pool)
	newInvoice := func(title string, amount float64) *ent.Invoice {
		t.Helper()
		invoice, err := invoiceService.Create(ctx, services.InvoiceCreateParams{CustomerID: customer.ID, Title: title, InvoiceDate: time.Now().UTC(), DueDate: time.Now().UTC(), LineItems: []services.LineItem{{Title: "Work", UnitPrice: amount, Quantity: 1}}})
		historyCheck(t, err)
		return invoice
	}
	paid := newInvoice("Original invoice", 100)
	partial := newInvoice("Credit application invoice", 30)
	actor := settlement.Actor{ID: src.actor, CompanyID: src.company, Role: "admin"}
	financial := settlement.New(src.pool)
	today := settlement.Date(time.Now().UTC().Format("2006-01-02"))
	oldPayment, err := financial.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "old-payment"}, InvoiceID: paid.ID, AmountCents: 2000, Method: settlement.Check, ReceivedDate: today, Reference: "voided-check"})
	historyCheck(t, err)
	_, err = financial.ReversePayment(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "reverse-old-payment"}, ID: oldPayment.ID, InvoiceID: paid.ID, EffectiveDate: today, Reason: "check replaced"})
	historyCheck(t, err)
	paymentRequest := settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "current-payment"}, InvoiceID: paid.ID, AmountCents: 15000, Method: settlement.Transfer, ReceivedDate: today, Reference: "bank-17", Notes: "Includes customer credit"}
	payment, err := financial.RecordPayment(ctx, actor, paymentRequest)
	historyCheck(t, err)
	if payment.AppliedCents != 10000 || payment.CreditCents != 5000 {
		t.Fatalf("payment split: %+v", payment)
	}
	creditView, err := financial.CustomerSettlement(ctx, actor, customer.ID)
	historyCheck(t, err)
	if len(creditView.Sources) != 1 || creditView.Sources[0].SourcePaymentID != payment.ID {
		t.Fatalf("credit provenance: %+v", creditView)
	}
	creditID := creditView.Sources[0].ID
	application, err := financial.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: "old-application"}, InvoiceID: partial.ID, CreditID: creditID, RequestedCents: 2000, EffectiveDate: today})
	historyCheck(t, err)
	refund, err := financial.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: "old-refund"}, CustomerID: customer.ID, AmountCents: 1000, Method: settlement.Check, EffectiveDate: today, Reason: "requested refund"})
	historyCheck(t, err)
	_, err = financial.ReverseRefund(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "reverse-refund"}, ID: refund.ID, CustomerID: customer.ID, EffectiveDate: today, Reason: "refund corrected"})
	historyCheck(t, err)
	_, err = financial.ReverseCreditApplication(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "reverse-application"}, ID: application.ID, InvoiceID: partial.ID, EffectiveDate: today, Reason: "application corrected"})
	historyCheck(t, err)
	_, err = financial.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: "current-application"}, InvoiceID: partial.ID, CreditID: creditID, RequestedCents: 1500, EffectiveDate: today})
	historyCheck(t, err)
	_, err = financial.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: "current-refund"}, CustomerID: customer.ID, AmountCents: 500, Method: settlement.Transfer, EffectiveDate: today, Reason: "correct refund", Reference: "refund-bank-17"})
	historyCheck(t, err)

	// Repeated conversion retains the original estimate, a hidden first invoice,
	// two independent cycle snapshots, and monotonically allocated invoice numbers.
	// EstimateService.Create currently omits company_id and fails the real
	// migration's status/company guard. Seed this initial document explicitly;
	// conversion, reversion, edits and subsequent reads still use real services.
	var draft int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='estimate:draft'`, src.company).Scan(&draft))
	estimate, err := src.client.Estimate.Create().SetCompanyID(src.company).SetCustomerID(customer.ID).SetStatusID(draft).SetTitle("Original proposal").SetNotes("Original snapshot notes").SetLineItems(`[{"title":"Quoted work","unit_price":42,"quantity":1}]`).Save(ctx)
	historyCheck(t, err)
	convertActor := conversion.Actor{ID: src.actor, CompanyID: src.company, Role: "admin"}
	conversions := conversion.New(src.pool)
	first, err := conversions.Convert(ctx, convertActor, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: estimate.ID})
	historyCheck(t, err)
	currentTitle := "Revised invoice carried into reconversion"
	_, err = invoiceService.Update(ctx, first.InvoiceID, services.InvoiceUpdateParams{Title: &currentTitle})
	historyCheck(t, err)
	reverted, err := conversions.Revert(ctx, convertActor, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: first.InvoiceID})
	historyCheck(t, err)
	if reverted.EstimateID != estimate.ID || !reverted.Reverted {
		t.Fatalf("revert lost original identity: %+v", reverted)
	}
	secondRequest := conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: estimate.ID}
	second, err := conversions.Convert(ctx, convertActor, secondRequest)
	historyCheck(t, err)
	if second.InvoiceID == first.InvoiceID || second.InvoiceNumber <= first.InvoiceNumber || second.CycleID == first.CycleID {
		t.Fatalf("conversion reused identity/number: first=%+v second=%+v", first, second)
	}
	timeline, err := conversions.Timeline(ctx, convertActor, estimate.ID)
	historyCheck(t, err)
	if len(timeline) < 3 {
		t.Fatalf("missing convert/revert activity: %+v", timeline)
	}

	// Complete one logical delivery after a permanent failure/manual retry, retain
	// actual provider evidence, and leave another immutable snapshot queued.
	outbox := delivery.New(src.pool, "https://history.example.test")
	deliveryActor := delivery.Actor{ID: src.actor, CompanyID: src.company, Role: "admin"}
	queue := func(invoiceID int64, title string) delivery.Delivery {
		t.Helper()
		d, err := outbox.Queue(ctx, deliveryActor, delivery.QueueRequest{Key: uuid.New(), Document: delivery.DocumentRef{Type: "invoice", ID: invoiceID}, Snapshot: delivery.Snapshot{To: []string{"original@example.test"}, CC: []string{"accounts@example.test"}, BCC: []string{"audit@example.test"}, Subject: title, TextBody: "Original text\nwith historical values", HTMLBody: "<p>Original <b>historical</b> values</p>", PDF: []byte("%PDF-1.7\n\x00\xffhistorical attachment\n%%EOF"), PDFFilename: "original-invoice.pdf"}})
		historyCheck(t, err)
		return d
	}
	delivered := queue(paid.ID, "Historical sent invoice")
	sender := &historyBackupSender{err: &delivery.SendError{Kind: delivery.SendPermanent, Err: errors.New("fixture permanent rejection")}}
	processed, err := outbox.ProcessOne(ctx, sender, delivery.NopAcceptanceHook{})
	historyCheck(t, err)
	if !processed || len(sender.sent) != 1 {
		t.Fatal("historical failure did not attempt delivery")
	}
	historyCheck(t, outbox.ManualRetry(ctx, deliveryActor, delivered.ID, "address verified by office"))
	sender.err = nil
	processed, err = outbox.ProcessOne(ctx, sender, delivery.NopAcceptanceHook{})
	historyCheck(t, err)
	if !processed || len(sender.sent) != 2 {
		t.Fatal("manual retry did not attempt original delivery")
	}
	historyAssertMessage(t, sender.sent[0], delivered)
	historyAssertMessage(t, sender.sent[1], delivered)
	evidence := delivery.ProviderEvent{CompanyID: src.company, DeliveryID: delivered.ID, ProviderIdentifier: "history-provider", EventID: "delivered-event-17", State: "delivered", Evidence: map[string]any{"event": "delivered", "recipient": "original@example.test", "receipt": "receipt-17"}}
	historyCheck(t, outbox.RecordProviderEvidence(ctx, evidence))
	pending := queue(partial.ID, "Pending original invoice")
	editedTitle := "Edited after queue; never rerender this on retry"
	_, err = invoiceService.Update(ctx, partial.ID, services.InvoiceUpdateParams{Title: &editedTitle})
	historyCheck(t, err)
	editedEmail := "edited-after-queue@example.test"
	_, err = services.NewCustomerService(src.client).Update(ctx, customer.ID, services.CustomerUpdateParams{Email: &editedEmail})
	historyCheck(t, err)

	// Public projections and all durable historical fields are captured before the
	// handler creates the archive. JSON includes exact bytea encodings, timestamps,
	// references, actors, fingerprints, allocations, snapshots and event evidence.
	beforeFinancial := historyFinancialView(t, src.pool, actor, customer.ID, paid.ID, partial.ID)
	beforeDelivered, err := outbox.History(ctx, src.company, delivery.DocumentRef{Type: "invoice", ID: paid.ID})
	historyCheck(t, err)
	if len(beforeDelivered) != 1 || beforeDelivered[0].State != "delivered" || beforeDelivered[0].LifetimeAttemptCount != 2 || beforeDelivered[0].DeliveredAt == nil {
		t.Fatalf("historical delivery: %+v", beforeDelivered)
	}
	tables := []string{"customers", "invoice_payments", "payment_invoice_allocations", "customer_credits", "credit_applications", "credit_refunds", "credit_refund_allocations", "settlement_reversals", "settlement_idempotency", "estimate_invoice_conversion_cycles", "estimate_invoice_conversion_operations", "estimates", "invoices", "document_deliveries", "document_delivery_events", "activity_logs"}
	before := historyDurableRows(t, src.pool, tables)
	src.roundTripTo(t, dst)
	historyCheck(t, dst.manager.RefreshConnections())
	for _, token := range []string{src.web, dst.web} {
		if _, err := dst.sessions.Validate(ctx, token); err == nil {
			t.Fatal("engine restore retained a source/destination web session")
		}
	}
	for _, token := range []string{src.mobile, dst.mobile} {
		if _, err := dst.sessions.ValidateMobile(ctx, token); err == nil {
			t.Fatal("engine restore retained a source/destination mobile session")
		}
	}
	historyEqual(t, "durable domain history", historyDurableRows(t, dst.pool, tables), before)
	historyEqual(t, "public settlement projections", historyFinancialView(t, dst.pool, actor, customer.ID, paid.ID, partial.ID), beforeFinancial)
	financial = settlement.New(dst.pool)
	replayed, err := financial.RecordPayment(ctx, actor, paymentRequest)
	historyCheck(t, err)
	historyEqual(t, "payment idempotency after restore", replayed, payment)
	conflict := paymentRequest
	conflict.AmountCents++
	if _, err := financial.RecordPayment(ctx, actor, conflict); !errors.Is(err, settlement.ErrIdempotencyConflict) {
		t.Fatalf("restored financial fingerprint accepted conflict: %v", err)
	}
	if _, err := financial.ReversePayment(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "blocked-after-restore"}, ID: payment.ID, InvoiceID: paid.ID, EffectiveDate: today, Reason: "must unwind dependants"}); !errors.Is(err, settlement.ErrDependency) {
		t.Fatalf("restored payment lost dependency protection: %v", err)
	}

	conversions = conversion.New(dst.pool)
	afterTimeline, err := conversions.Timeline(ctx, convertActor, estimate.ID)
	historyCheck(t, err)
	historyEqual(t, "conversion timeline", afterTimeline, timeline)
	secondReplay, err := conversions.Convert(ctx, convertActor, secondRequest)
	historyCheck(t, err)
	historyEqual(t, "conversion idempotency", secondReplay, second)
	if _, err := services.NewEstimateService(dst.client).GetByID(ctx, estimate.ID); !ent.IsNotFound(err) {
		t.Fatalf("estimate tombstone visible through service: %v", err)
	}
	if _, err := services.NewInvoiceService(dst.client).GetByID(ctx, first.InvoiceID); !ent.IsNotFound(err) {
		t.Fatalf("first invoice tombstone visible through service: %v", err)
	}
	liveInvoice, err := services.NewInvoiceService(dst.client).GetByID(ctx, second.InvoiceID)
	historyCheck(t, err)
	if liveInvoice.Title != currentTitle {
		t.Fatalf("reconversion lost current document values: %+v", liveInvoice)
	}
	for _, id := range []int64{first.InvoiceID, second.InvoiceID} {
		root, err := conversions.ActivityEstimateID(ctx, convertActor, objectref.New(objectref.TypeInvoice, id))
		historyCheck(t, err)
		if root != estimate.ID {
			t.Fatalf("invoice %d lost provenance root: %d", id, root)
		}
	}
	// Verify restored triggers, not just restored rows. Each statement addresses a
	// known record and must fail specifically for immutable-history protection.
	for _, mutation := range []string{
		`UPDATE invoice_payments SET notes='tampered'`,
		fmt.Sprintf(`UPDATE invoices SET title='tampered' WHERE id=%d`, first.InvoiceID),
		`UPDATE estimate_invoice_conversion_cycles SET source_snapshot='{}'`,
		`UPDATE document_deliveries SET subject='tampered'`,
	} {
		if _, err := dst.pool.Exec(ctx, mutation); err == nil || !strings.Contains(err.Error(), "immutable") {
			t.Fatalf("restored immutable guard: %s: %v", mutation, err)
		}
	}

	// Reopen control from disk to prove the pause survives process initialization.
	reopened, err := instancecontrol.New(dst.control.StateDir())
	historyCheck(t, err)
	if !reopened.EmailDisabled() {
		t.Fatal("restore did not durably pause outbound delivery")
	}
	outbox = delivery.New(dst.pool, "https://destination.example.test")
	outbox.SetInstanceControl(reopened)
	outbox.SetBeforeWork(dst.manager.RefreshConnections)
	afterDelivered, err := outbox.History(ctx, src.company, delivery.DocumentRef{Type: "invoice", ID: paid.ID})
	historyCheck(t, err)
	historyEqual(t, "delivery history and evidence state", afterDelivered, beforeDelivered)
	restoredSender := &historyBackupSender{}
	for range 2 {
		processed, err := outbox.ProcessOne(ctx, restoredSender, delivery.NopAcceptanceHook{})
		historyCheck(t, err)
		if processed || len(restoredSender.sent) != 0 {
			t.Fatal("restored pending work sent before explicit enable")
		}
	}
	historyEqual(t, "paused work and replay leave history untouched", historyDurableRows(t, dst.pool, tables), before)
	historyCheck(t, reopened.EnableEmail())
	processed, err = outbox.ProcessOne(ctx, restoredSender, delivery.NopAcceptanceHook{})
	historyCheck(t, err)
	if !processed || len(restoredSender.sent) != 1 {
		t.Fatal("explicit enable did not resume pending delivery")
	}
	historyAssertMessage(t, restoredSender.sent[0], pending)
	pendingHistory, err := outbox.History(ctx, src.company, delivery.DocumentRef{Type: "invoice", ID: partial.ID})
	historyCheck(t, err)
	if len(pendingHistory) != 1 || pendingHistory[0].ID != pending.ID || pendingHistory[0].State != "accepted" || pendingHistory[0].DeliveredAt != nil || pendingHistory[0].LifetimeAttemptCount != 1 {
		t.Fatalf("SMTP acceptance incorrectly implies delivery: %+v", pendingHistory)
	}
	eventsBefore := historyDurableRows(t, dst.pool, []string{"document_delivery_events"})
	historyCheck(t, outbox.RecordProviderEvidence(ctx, evidence))
	historyEqual(t, "provider evidence deduplication after restore", historyDurableRows(t, dst.pool, []string{"document_delivery_events"}), eventsBefore)

	// A third real conversion proves the restored identity and business-number
	// sequences advance past historical tombstones rather than merely looking right.
	_, err = conversions.Revert(ctx, convertActor, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: second.InvoiceID})
	historyCheck(t, err)
	restoredEstimate, err := services.NewEstimateService(dst.client).GetByID(ctx, estimate.ID)
	historyCheck(t, err)
	if restoredEstimate.ID != estimate.ID || restoredEstimate.Title != currentTitle {
		t.Fatalf("post-restore reversion lost identity/current values: %+v", restoredEstimate)
	}
	third, err := conversions.Convert(ctx, convertActor, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: estimate.ID})
	historyCheck(t, err)
	if third.InvoiceID <= second.InvoiceID || third.InvoiceNumber <= second.InvoiceNumber || third.CycleID == first.CycleID || third.CycleID == second.CycleID {
		t.Fatalf("restored sequences reused history: first=%+v second=%+v third=%+v", first, second, third)
	}
}

type historyBackupInstance struct {
	role, name, web, mobile, csrf string
	company, actor                int64
	pool                          *pgxpool.Pool
	client                        *ent.Client
	manager                       *backup.Manager
	control                       *instancecontrol.Control
	sessions                      *services.SessionService
	router                        http.Handler
	cookie                        *http.Cookie
}

func newHistoryBackupInstance(t *testing.T, admin *pgx.Conn, dsn, name string) *historyBackupInstance {
	t.Helper()
	ctx := context.Background()
	f := &historyBackupInstance{name: name, role: "backup_history_" + strings.ReplaceAll(uuid.NewString(), "-", "")}
	ident := pgx.Identifier{f.role}.Sanitize()
	_, err := admin.Exec(ctx, "CREATE ROLE "+ident+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS")
	historyCheck(t, err)
	t.Cleanup(func() { _, err := admin.Exec(ctx, "DROP ROLE "+ident); historyCheck(t, err) })
	_, err = admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER "+ident+" TEMPLATE template0 ENCODING 'UTF8'")
	historyCheck(t, err)
	t.Cleanup(func() { _, err := admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)"); historyCheck(t, err) })
	u, err := url.Parse(dsn)
	historyCheck(t, err)
	u.User, u.Path = url.User(f.role), "/"+f.role
	db, err := database.Connect(ctx, u.String())
	historyCheck(t, err)
	t.Cleanup(db.Close)
	f.pool = db.Pool
	var privileged bool
	historyCheck(t, f.pool.QueryRow(ctx, `SELECT rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&privileged))
	if privileged {
		t.Fatal("backup must run as a nonprivileged application database owner")
	}
	root := t.TempDir()
	f.control, err = instancecontrol.New(filepath.Join(root, "state"))
	historyCheck(t, err)
	uploads := filepath.Join(root, "uploads")
	historyCheck(t, os.MkdirAll(uploads, 0700))
	f.manager, err = backup.New(backup.Config{DSN: u.String(), UploadDir: uploads, StateDir: f.control.StateDir(), Version: "v1.2.3", Commit: "abcdef1234567", Control: f.control, ResetConnections: db.Pool.Reset})
	historyCheck(t, err)
	t.Cleanup(f.manager.Shutdown)
	historyCheck(t, f.manager.Recover(ctx))
	historyCheck(t, db.Migrate(ctx, database.MigrationFS()))
	pgcfg, err := pgx.ParseConfig(u.String())
	historyCheck(t, err)
	pgcfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	pgcfg.StatementCacheCapacity, pgcfg.DescriptionCacheCapacity = 0, 0
	f.client = ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, stdlib.OpenDB(*pgcfg))))
	t.Cleanup(func() { historyCheck(t, f.client.Close()) })
	historyCheck(t, f.pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES($1,$2) RETURNING id`, name, f.role).Scan(&f.company))
	hash, err := bcrypt.GenerateFromPassword([]byte("history-account-secret"), bcrypt.MinCost)
	historyCheck(t, err)
	user, err := f.client.User.Create().SetCompanyID(f.company).SetName("Historical administrator").SetEmail(f.role + "@example.test").SetPasswordHash(string(hash)).SetRole("admin").SetIsActive(true).Save(ctx)
	historyCheck(t, err)
	f.actor = user.ID
	settings, err := f.client.CompanySettings.Query().Only(ctx)
	historyCheck(t, err)
	_, err = f.client.CompanySettings.UpdateOne(settings).SetCompanyID(f.company).SetBusinessName(name).SetTimezone("UTC").SetNextInvoiceNumber(10).Save(ctx)
	historyCheck(t, err)
	f.sessions = services.NewSessionService(f.pool)
	f.web, err = f.sessions.Create(ctx, f.actor)
	historyCheck(t, err)
	f.mobile, err = f.sessions.CreateMobile(ctx, f.actor, "history phone")
	historyCheck(t, err)
	f.router = NewBackupRouter(f.manager, f.control, services.NewUserService(f.client), services.NewCompanySettingsService(f.client), f.sessions, "", backupTestActivityHandler(f.client))
	r := httptest.NewRequest(http.MethodGet, "/settings/backup", nil)
	r.AddCookie(&http.Cookie{Name: "session", Value: f.web})
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("backup page: %d %s", w.Code, w.Body)
	}
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == "csrf_token" {
			f.cookie = cookie
		}
	}
	match := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if len(match) != 2 || f.cookie == nil {
		t.Fatal("missing real CSRF token")
	}
	f.csrf = html.UnescapeString(match[1])
	return f
}

func (f *historyBackupInstance) request(action string, body io.Reader, raw bool, capability string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/settings/backup/"+action, body)
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-CSRF-Token", f.csrf)
	r.AddCookie(f.cookie)
	if capability != "" {
		r.Header.Set("X-Backup-Operation", capability)
	} else {
		r.AddCookie(&http.Cookie{Name: "session", Value: f.web})
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if raw {
		r.Header.Set("Content-Type", "application/octet-stream")
		r.Header.Set("X-Archive-Password", `"history-archive-secret"`)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	return w
}

func (f *historyBackupInstance) operation(t *testing.T, w *httptest.ResponseRecorder) backup.Operation {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		if w.Code != http.StatusOK {
			t.Fatalf("backup operation HTTP %d: %s", w.Code, w.Body)
		}
		var op backup.Operation
		historyCheck(t, json.Unmarshal(w.Body.Bytes(), &op))
		switch op.Phase {
		case "ready", "complete":
			return op
		case "failed":
			t.Fatalf("backup operation failed: %+v", op)
		}
		if time.Now().After(deadline) {
			t.Fatalf("backup operation timed out: %+v", op)
		}
		time.Sleep(20 * time.Millisecond)
		w = f.request("status", nil, false, op.ID)
	}
}

func (f *historyBackupInstance) roundTripTo(t *testing.T, dst *historyBackupInstance) {
	t.Helper()
	op := f.operation(t, f.request("create", strings.NewReader(url.Values{"archive_password": {"history-archive-secret"}}.Encode()), false, ""))
	archive := f.request("download", strings.NewReader(url.Values{"operation": {op.ID}, "current_password": {"history-account-secret"}}.Encode()), false, "")
	if archive.Code != http.StatusOK || !strings.Contains(archive.Header().Get("Content-Disposition"), ".age") || !bytes.HasPrefix(archive.Body.Bytes(), []byte("age-encryption.org/v1")) {
		t.Fatalf("encrypted handler download: status=%d headers=%v", archive.Code, archive.Header())
	}
	op = dst.operation(t, dst.request("upload", bytes.NewReader(archive.Body.Bytes()), true, ""))
	if op.SourceName != f.name || op.ArchiveDigest == "" {
		t.Fatalf("archive review did not identify source: %+v", op)
	}
	op = dst.operation(t, dst.request("restore", strings.NewReader(url.Values{"operation": {op.ID}, "archive_digest": {op.ArchiveDigest}, "current_password": {"history-account-secret"}, "confirmation": {dst.name}}.Encode()), false, ""))
	if op.Phase != "complete" {
		t.Fatalf("restore incomplete: %+v", op)
	}
}

// ReversalKey is a newly generated form token, not persisted financial history.
// Normalize only those tokens; compare every other public projection field.
func historyFinancialView(t *testing.T, pool *pgxpool.Pool, actor settlement.Actor, customer, paid, partial int64) []byte {
	t.Helper()
	svc := settlement.New(pool)
	ctx := context.Background()
	a, err := svc.InvoiceSettlement(ctx, actor, paid)
	historyCheck(t, err)
	b, err := svc.InvoiceSettlement(ctx, actor, partial)
	historyCheck(t, err)
	c, err := svc.CustomerSettlement(ctx, actor, customer)
	historyCheck(t, err)
	if a.State != settlement.Paid || a.TotalCents != 10000 || a.SettledCents != 10000 || a.AmountDueCents != 0 || b.State != settlement.PartiallyPaid || b.SettledCents != 1500 || b.AmountDueCents != 1500 || c.AvailableCreditCents != 3000 {
		t.Fatalf("financial projection/conservation mismatch: paid=%+v partial=%+v credit=%+v", a, b, c)
	}
	var activeRefunds int64
	for i := range c.Refunds {
		c.Refunds[i].ReversalKey = ""
		if c.Refunds[i].Reversal == nil {
			activeRefunds += c.Refunds[i].AmountCents
		}
	}
	if 15000 != a.SettledCents+b.SettledCents+activeRefunds+c.AvailableCreditCents || activeRefunds != 500 {
		t.Fatalf("payment value was not conserved: refund=%d credit=%d", activeRefunds, c.AvailableCreditCents)
	}
	for _, invoice := range []*settlement.InvoiceSettlement{&a, &b} {
		for i := range invoice.Payments {
			invoice.Payments[i].ReversalKey = ""
		}
		for i := range invoice.Applications {
			invoice.Applications[i].ReversalKey = ""
		}
	}
	for i := range c.Applications {
		c.Applications[i].ReversalKey = ""
	}
	encoded, err := json.Marshal([]any{a, b, c})
	historyCheck(t, err)
	return encoded
}

func historyDurableRows(t *testing.T, pool *pgxpool.Pool, tables []string) map[string]string {
	t.Helper()
	out := make(map[string]string, len(tables))
	for _, table := range tables {
		var rows string
		query := `SELECT coalesce(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + pgx.Identifier{table}.Sanitize() + ` t`
		historyCheck(t, pool.QueryRow(context.Background(), query).Scan(&rows))
		if rows == "[]" {
			t.Fatalf("historical fixture did not populate %s", table)
		}
		out[table] = rows
	}
	return out
}

type historyBackupSender struct {
	sent []delivery.Delivery
	err  error
}

func (s *historyBackupSender) Send(_ context.Context, d delivery.Delivery) (delivery.SendResult, error) {
	s.sent = append(s.sent, d)
	return delivery.SendResult{ProviderIdentifier: "history-provider", Evidence: map[string]any{"accepted": true, "receipt": "handoff-17"}}, s.err
}

func historyAssertMessage(t *testing.T, got, want delivery.Delivery) {
	t.Helper()
	// Attempt/state fields legitimately change; the complete message must not.
	message := func(d delivery.Delivery) any {
		return struct {
			ID        int64
			MessageID string
			Snapshot  delivery.Snapshot
		}{d.ID, d.MessageID, delivery.Snapshot{To: d.To, CC: d.CC, BCC: d.BCC, Subject: d.Subject, TextBody: d.TextBody, HTMLBody: d.HTMLBody, PDF: d.PDF, PDFFilename: d.PDFFilename}}
	}
	historyEqual(t, "immutable delivery bytes and stable identity", message(got), message(want))
}

func historyCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func historyEqual(t *testing.T, label string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s differs:\ngot: %#v\nwant: %#v", label, got, want)
	}
}
