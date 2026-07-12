package delivery

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeSender struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeSender) Send(_ context.Context, d Delivery) (SendResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if d.MessageID == "" || !strings.HasPrefix(string(d.PDF), "pdf-v1") {
		return SendResult{}, errors.New("snapshot changed")
	}
	return SendResult{ProviderIdentifier: "fake-1", Evidence: map[string]any{"accepted": true}}, f.err
}

type fakeHook struct {
	calls   int
	changed bool
}

func (h *fakeHook) OnAccepted(ctx context.Context, tx pgx.Tx, d Delivery) error {
	h.calls++
	var current *int64
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT status_id FROM %ss WHERE id=$1`, d.DocumentType), d.DocumentID).Scan(&current); err != nil {
		return err
	}
	if sameStatus(current, d.ExpectedStatusID) {
		h.changed = true
		_, err := tx.Exec(ctx, `INSERT INTO activity_logs(company_id,actor_id,action,object_type,object_id) VALUES($1,$2,'fake_sent_automation',$3,$4)`, d.CompanyID, d.ActorID, d.DocumentType, d.DocumentID)
		return err
	}
	return nil
}
func sameStatus(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func TestQueueWorkerTrackingAndEvidenceIntegration(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "https://example.test")
	largePDF := make([]byte, 2<<20)
	copy(largePDF, "pdf-v1")
	request := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"customer@example.test"}, Subject: "Frozen", TextBody: strings.Repeat("body-v1", 10000), HTMLBody: "<html><body>" + strings.Repeat("body-v1", 10000) + "</body></html>", PDF: largePDF, PDFFilename: "estimate.pdf"}}
	d, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, request)
	if err != nil || again.ID != d.ID {
		t.Fatalf("idempotent queue = %d, %v", again.ID, err)
	}
	conflict := request
	conflict.Snapshot.Subject = "Different"
	if _, err = svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, conflict); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}
	if !strings.Contains(d.HTMLBody, "/delivery/open/") || strings.Contains(d.HTMLBody, "customer@example.test\"") {
		t.Fatalf("tracking HTML = %q", d.HTMLBody)
	}
	token := strings.Split(strings.Split(d.HTMLBody, "/delivery/open/")[1], `"`)[0]
	if err = svc.RecordOpen(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordOpen(ctx, token); err != nil {
		t.Fatal(err)
	}
	var openCount int
	var storedToken string
	if err = f.db.Pool.QueryRow(ctx, `SELECT open_count,encode(tracking_token_hash,'hex') FROM document_deliveries WHERE id=$1`, d.ID).Scan(&openCount, &storedToken); err != nil || openCount != 2 || storedToken == token {
		t.Fatalf("tracking counters/hash = %d %q %v", openCount, storedToken, err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET subject='mutated' WHERE id=$1`, d.ID); err == nil {
		t.Fatal("snapshot update succeeded")
	}
	if _, err = svc.History(ctx, f.otherCompany, request.Document); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{}
	hook := &fakeHook{}
	ok, err := svc.ProcessOne(ctx, sender, hook)
	if err != nil || !ok || hook.calls != 1 || !hook.changed {
		t.Fatalf("process = %v %v, hook=%+v", ok, err, hook)
	}
	history, err := svc.History(ctx, f.company, request.Document)
	if err != nil || len(history) != 1 || history[0].State != "accepted" {
		t.Fatalf("history = %+v, %v", history, err)
	}
	for _, field := range []string{"PDF", "TextBody", "HTMLBody"} {
		if _, ok := reflect.TypeOf(history[0]).FieldByName(field); ok {
			t.Fatalf("history summary exposes snapshot field %s", field)
		}
	}
	provider := ProviderEvent{CompanyID: f.company, DeliveryID: d.ID, State: "delivered", ProviderIdentifier: "fake-1", EventID: "evt-1", Evidence: map[string]any{"event": "delivered"}}
	if err = svc.RecordProviderEvidence(ctx, provider); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordProviderEvidence(ctx, provider); err != nil {
		t.Fatalf("dedupe: %v", err)
	}
	provider.CompanyID = f.otherCompany
	provider.EventID = "evt-2"
	if err = svc.RecordProviderEvidence(ctx, provider); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-company evidence = %v", err)
	}
	var queuedActivity, acceptedActivity int
	if err = f.db.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE action='document_delivery_queued'),count(*) FILTER(WHERE action='document_delivery_accepted') FROM activity_logs WHERE company_id=$1 AND object_type='estimate' AND object_id=$2`, f.company, f.estimate).Scan(&queuedActivity, &acceptedActivity); err != nil || queuedActivity != 1 || acceptedActivity != 1 {
		t.Fatalf("activity = %d/%d %v", queuedActivity, acceptedActivity, err)
	}
}

func TestAuthorizationRetryConcurrencyAndStaleStatusIntegration(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	actor := Actor{ID: f.tech, CompanyID: f.company, Role: "tech"}
	base := QueueRequest{Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"x@y.test"}, Subject: "S", TextBody: "B", PDF: []byte("pdf-v1"), PDFFilename: "x.pdf"}}
	base.Key = uuid.New()
	if _, err := svc.Queue(ctx, actor, base); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unassigned tech = %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, f.tech); err != nil {
		t.Fatal(err)
	}
	base.Key = uuid.New()
	d, err := svc.Queue(ctx, actor, base)
	if err != nil {
		t.Fatal(err)
	}
	claims := make(chan Delivery, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := svc.Claim(ctx)
			if e != nil {
				errs <- e
				return
			}
			claims <- c
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	if len(claims) != 1 {
		t.Fatalf("concurrent claims = %d", len(claims))
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET lease_expires_at=now()-interval '1 second' WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	c, err := svc.Claim(ctx)
	if err != nil || c.ID != d.ID || c.CycleAttemptCount != 2 {
		t.Fatalf("stale claim = %+v %v", c, err)
	}
	sender := &fakeSender{err: errors.New("temporary")}
	if err = svc.failAttempt(ctx, c, sender.err); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET next_attempt_at=now() WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	c, err = svc.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET cycle_attempt_count=$1 WHERE id=$2`, MaxAttempts, d.ID); err != nil {
		t.Fatal(err)
	}
	c.CycleAttemptCount = MaxAttempts
	if err = svc.failAttempt(ctx, c, errors.New("final")); err != nil {
		t.Fatal(err)
	}
	if err = svc.ManualRetry(ctx, actor, d.ID, "operator requested retry"); err != nil {
		t.Fatal(err)
	}
	var lifetime, retryCycle int
	var retryEvents, retryActivities int
	if err = f.db.Pool.QueryRow(ctx, `SELECT lifetime_attempt_count,retry_cycle FROM document_deliveries WHERE id=$1`, d.ID).Scan(&lifetime, &retryCycle); err != nil || lifetime < 3 || retryCycle != 1 {
		t.Fatalf("retry counters=%d/%d %v", lifetime, retryCycle, err)
	}
	_ = f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM document_delivery_events WHERE delivery_id=$1 AND actor_id=$2 AND evidence->>'reason'='operator requested retry'`, d.ID, f.tech).Scan(&retryEvents)
	_ = f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE action='document_delivery_retried' AND object_id=$1`, f.estimate).Scan(&retryActivities)
	if retryEvents != 1 || retryActivities != 1 {
		t.Fatalf("manual retry provenance=%d/%d", retryEvents, retryActivities)
	}
	// A status change after queueing makes the hook deliberately skip automation.
	var otherStatus int64
	if err = f.db.Pool.QueryRow(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) SELECT company_id,workflow_id,'Changed','estimate:estimate',2,false FROM statuses WHERE id=$1 RETURNING id`, f.status).Scan(&otherStatus); err != nil {
		t.Fatal(err)
	}
	tx, txErr := f.db.Pool.Begin(ctx)
	if txErr != nil {
		t.Fatal(txErr)
	}
	if _, txErr = tx.Exec(ctx, `SELECT set_config('freefsm.status_transition','allowed',true)`); txErr == nil {
		_, txErr = tx.Exec(ctx, `UPDATE estimates SET status_id=$1 WHERE id=$2`, otherStatus, f.estimate)
	}
	if txErr == nil {
		txErr = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if txErr != nil {
		t.Fatal(txErr)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET next_attempt_at=now() WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	hook := &fakeHook{}
	sender.err = nil
	if _, err = svc.ProcessOne(ctx, sender, hook); err != nil || hook.changed {
		t.Fatalf("stale hook changed=%v err=%v", hook.changed, err)
	}
}

func TestLeaseCASMaxRecoveryAndStateGuardIntegration(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	actor := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"x@y.test"}, Subject: "S", TextBody: "B", PDF: []byte("pdf-v1"), PDFFilename: "x.pdf"}}
	d, err := svc.Queue(ctx, actor, r)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	newToken := uuid.New()
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET attempt_token=$1 WHERE id=$2`, newToken, d.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.failAttempt(ctx, claimed, errors.New("late worker")); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale CAS=%v", err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET cycle_attempt_count=$1,lease_expires_at=now()-interval '1 second' WHERE id=$2`, MaxAttempts, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Claim(ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("claim after max stale=%v", err)
	}
	var state string
	var crashEvents int
	if err = f.db.Pool.QueryRow(ctx, `SELECT state FROM document_deliveries WHERE id=$1`, d.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatalf("max stale state=%q %v", state, err)
	}
	_ = f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM document_delivery_events WHERE delivery_id=$1 AND evidence->>'reason'='lease_expired'`, d.ID).Scan(&crashEvents)
	if crashEvents != 1 {
		t.Fatalf("crash events=%d", crashEvents)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET state='queued',failed_at=NULL WHERE id=$1`, d.ID); err == nil {
		t.Fatal("unguarded state transition succeeded")
	}
}

func TestRetryAndTrackingHelpers(t *testing.T) {
	if retryDelay(1) != time.Minute || retryDelay(99) != time.Hour {
		t.Fatal("retry bounds")
	}
	h := TrackingResponseHeaders()
	if !strings.Contains(h["Cache-Control"], "no-store") || len(TrackingPixelGIF()) == 0 {
		t.Fatal("tracking response")
	}
}

func TestTrackingHandlerRejectsHEADAndSetsPrivacyHeaders(t *testing.T) {
	r := chi.NewRouter()
	svc := New(nil, "")
	r.Get("/delivery/open/{token}", svc.OpenHandler)
	req := httptest.NewRequest(http.MethodHead, "/delivery/open/"+strings.Repeat("a", 43), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("HEAD counted as tracking open")
	}
	req = httptest.NewRequest(http.MethodGet, "/delivery/open/short", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("tracking response=%d %#v", w.Code, w.Header())
	}
}

type deliveryFixture struct {
	db                                                        *database.DB
	company, otherCompany, admin, tech, job, estimate, status int64
}

func newDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL delivery tests")
	}
	ctx := context.Background()
	adminPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	schema := fmt.Sprintf("freefsm_delivery_%d", time.Now().UnixNano())
	if _, err = adminPool.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = adminPool.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`) })
	db, err := database.Connect(ctx, deliverySearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err = db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	f := &deliveryFixture{db: db}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Delivery','delivery') RETURNING id`).Scan(&f.company); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other','other') RETURNING id`).Scan(&f.otherCompany); err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, `INSERT INTO company_settings(company_id,business_name,email_tracking_enabled) VALUES($1,'Delivery',true),($2,'Other',false)`, f.company, f.otherCompany)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'admin@d.test','x','Admin','admin') RETURNING id`, f.company).Scan(&f.admin); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'tech@d.test','x','Tech','tech') RETURNING id`, f.company).Scan(&f.tech); err != nil {
		t.Fatal(err)
	}
	var customer, workflow int64
	_ = db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, f.company).Scan(&customer)
	_ = db.Pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Work') RETURNING id`, f.company, customer).Scan(&f.job)
	_ = db.Pool.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Estimate','estimate') RETURNING id`, f.company).Scan(&workflow)
	_, _ = db.Pool.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES
	 ($1,$2,'Draft','estimate:draft',1,true),($1,$2,'Estimate','estimate:estimate',1,true),($1,$2,'Sent','estimate:sent',1,true),
	 ($1,$2,'Accepted','estimate:accepted',1,true),($1,$2,'Rejected','estimate:rejected',1,true),($1,$2,'Completed','estimate:completed',1,true)`, f.company, workflow)
	_ = db.Pool.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key='estimate:draft'`, workflow).Scan(&f.status)
	if err = db.Pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,status_id,title) VALUES($1,$2,$3,$4,'Estimate') RETURNING id`, f.company, customer, f.job, f.status).Scan(&f.estimate); err != nil {
		t.Fatal(err)
	}
	return f
}
func deliverySearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + "?search_path=" + schema
}
