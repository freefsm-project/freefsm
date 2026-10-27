package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestProviderTrustedAdapterExactAttemptAndMessage(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	a := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	d, err := svc.Queue(ctx, a, QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"owned@example.test"}, Subject: "Provider", PDF: []byte("pdf-v1"), PDFFilename: "x.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ProcessOne(ctx, providerSender{"reused-provider-id"}, nil); err != nil {
		t.Fatal(err)
	}
	history, err := svc.History(ctx, f.company, DocumentRef{Type: "estimate", ID: f.estimate})
	if err != nil {
		t.Fatal(err)
	}
	var first uuid.UUID
	for _, event := range history[0].Events {
		if event.State == "accepted" {
			first = uuid.MustParse(event.Attempt)
		}
	}
	p := ProviderEvent{CompanyID: f.company, DeliveryID: d.ID, ProviderIdentifier: "reused-provider-id", EventID: "bounce", State: "bounced", AttemptToken: first, MessageID: d.MessageID, Evidence: map[string]any{"provider": "owned local fixture", "outcome": "bounced"}}
	// The recorder is a trusted adapter port, never an unauthenticated product
	// route. This owned local adapter exercises the authentication precondition.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owned-provider-secret" {
			http.Error(w, "unauthenticated", 401)
			return
		}
		var event ProviderEvent
		if json.NewDecoder(r.Body).Decode(&event) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		err := svc.RecordProviderEvidence(r.Context(), event)
		switch {
		case errors.Is(err, ErrForbidden):
			http.Error(w, "uncorrelated", 403)
		case errors.Is(err, ErrIdempotencyConflict):
			http.Error(w, "changed duplicate", 409)
		case err != nil:
			http.Error(w, err.Error(), 422)
		default:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	post := func(event ProviderEvent, auth string, want int) {
		t.Helper()
		raw, _ := json.Marshal(event)
		req, _ := http.NewRequest("POST", server.URL, bytes.NewReader(raw))
		req.Header.Set("Authorization", auth)
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("provider status=%d want %d", resp.StatusCode, want)
		}
	}
	post(p, "", 401)
	wrong := p
	wrong.MessageID = "<unrelated>"
	post(wrong, "Bearer owned-provider-secret", 403)
	wrong = p
	wrong.AttemptToken = uuid.New()
	post(wrong, "Bearer owned-provider-secret", 403)
	post(p, "Bearer owned-provider-secret", 204)
	post(p, "Bearer owned-provider-secret", 204)
	if err = svc.ManualRetry(ctx, a, d.ID, "reviewed provider bounce"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ProcessOne(ctx, providerSender{"reused-provider-id"}, nil); err != nil {
		t.Fatal(err)
	}
	p.EventID = "late-first"
	p.State = "delivered"
	p.Evidence = map[string]any{"provider": "owned local fixture", "outcome": "delivered"}
	post(p, "Bearer owned-provider-secret", 204)
	ambiguous := p
	ambiguous.EventID = "no-attempt"
	ambiguous.AttemptToken = uuid.Nil
	post(ambiguous, "Bearer owned-provider-secret", 403)
	history, err = svc.History(ctx, f.company, DocumentRef{Type: "estimate", ID: f.estimate})
	if err != nil {
		t.Fatal(err)
	}
	if history[0].State != "accepted" {
		t.Fatal("old-attempt outcome changed retry")
	}
	var latest uuid.UUID
	for _, event := range history[0].Events {
		if event.State == "accepted" {
			latest = uuid.MustParse(event.Attempt)
		}
	}
	p.AttemptToken = latest
	p.EventID = "current-delivered"
	post(p, "Bearer owned-provider-secret", 204)
	p.State = "bounced"
	p.EventID = "current-bounced"
	p.Evidence = map[string]any{"provider": "owned local fixture", "outcome": "bounced"}
	post(p, "Bearer owned-provider-secret", 204)
	p.State = "delivered"
	p.EventID = "out-of-order"
	post(p, "Bearer owned-provider-secret", 204)
	history, err = svc.History(ctx, f.company, DocumentRef{Type: "estimate", ID: f.estimate})
	if err != nil {
		t.Fatal(err)
	}
	if history[0].State != "bounced" {
		t.Fatal("delayed delivered evidence erased bounce")
	}
	if worked, err := svc.ProcessOne(ctx, providerSender{"must-not-send"}, nil); err != nil || worked {
		t.Fatalf("automatic bounce retry=%v %v", worked, err)
	}
}

type providerSender struct{ id string }

func TestProviderLegacyAcceptanceKeepsAttemptUnknown(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	d, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"owned@example.test"}, Subject: "Legacy provider", PDF: []byte("pdf-v1"), PDFFilename: "x.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	var legacy int64
	err = f.db.Pool.QueryRow(ctx, `INSERT INTO document_deliveries(company_id,document_type,document_id,estimate_id,actor_id,idempotency_key,request_fingerprint,recipients_to,subject,text_body,pdf_data,pdf_filename,message_id,sender_address,state,lifetime_attempt_count,accepted_at,provider_identifier) SELECT company_id,document_type,document_id,estimate_id,actor_id,gen_random_uuid(),request_fingerprint,recipients_to,subject,text_body,pdf_data,pdf_filename,message_id||'.legacy',sender_address,'accepted',1,clock_timestamp(),'legacy-provider' FROM document_deliveries WHERE id=$1 RETURNING id`, d.ID).Scan(&legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `INSERT INTO document_delivery_events(delivery_id,company_id,event_kind,to_state) VALUES($1,$2,'state','accepted')`, legacy, f.company); err != nil {
		t.Fatal(err)
	}
	p := ProviderEvent{CompanyID: f.company, DeliveryID: legacy, ProviderIdentifier: "legacy-provider", EventID: "authentic-new-event", State: "delivered", Evidence: map[string]any{"provider": "new delivery evidence"}}
	if err = svc.RecordProviderEvidence(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = svc.RecordProviderEvidence(ctx, p); err != nil {
		t.Fatal(err)
	}
	var state string
	var unknown bool
	if err = f.db.Pool.QueryRow(ctx, `SELECT d.state,e.attempt_token IS NULL AND e.transport_event_id IS NULL FROM document_deliveries d JOIN document_delivery_events e ON e.delivery_id=d.id WHERE d.id=$1 AND e.event_kind='provider'`, legacy).Scan(&state, &unknown); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" || !unknown {
		t.Fatal("legacy correlation invented historical attempt facts")
	}
}

func TestProviderEvidenceBeforeAcceptanceIsRetainedAndReconciled(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	_, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"owned@example.test"}, Subject: "Provider", PDF: []byte("pdf-v1"), PDFFilename: "x.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	var p ProviderEvent
	_, err = svc.ProcessOne(ctx, senderFunc(func(ctx context.Context, d Delivery) (SendResult, error) {
		p = ProviderEvent{CompanyID: d.CompanyID, DeliveryID: d.ID, ProviderIdentifier: "owned-early", EventID: "early-delivered", State: "delivered", AttemptToken: d.AttemptToken, MessageID: d.MessageID, Evidence: map[string]any{"authenticated_provider": "delivered before response"}}
		if err := svc.RecordProviderEvidence(ctx, p); err != nil {
			return SendResult{}, err
		}
		return SendResult{ProviderIdentifier: "owned-early", Evidence: map[string]any{"accepted": true}}, nil
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	history, err := svc.History(ctx, f.company, DocumentRef{Type: "estimate", ID: f.estimate})
	if err != nil || history[0].State != "delivered" {
		t.Fatalf("early evidence lost: %+v %v", history, err)
	}
	if err = svc.RecordProviderEvidence(ctx, p); err != nil {
		t.Fatalf("early duplicate changed correlation: %v", err)
	}
}

func (s providerSender) Send(_ context.Context, _ Delivery) (SendResult, error) {
	return SendResult{ProviderIdentifier: s.id, Evidence: map[string]any{"receipt": s.id}}, nil
}

func TestProviderDelayedEvidenceRetainsOriginalAttempt(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	a := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	d, err := svc.Queue(ctx, a, QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"recipient@example.test"}, Subject: "S", PDF: []byte("pdf-v1"), PDFFilename: "x.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ProcessOne(ctx, providerSender{"attempt-one"}, nil); err != nil {
		t.Fatal(err)
	}
	p := ProviderEvent{CompanyID: f.company, DeliveryID: d.ID, ProviderIdentifier: "attempt-one", EventID: "bounce-one", State: "bounced", Evidence: map[string]any{"raw": "authenticated bounce"}}
	if err = svc.RecordProviderEvidence(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err = svc.ManualRetry(ctx, a, d.ID, "reviewed bounce"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ProcessOne(ctx, providerSender{"attempt-two"}, nil); err != nil {
		t.Fatal(err)
	}
	p.EventID = "delayed-one"
	p.State = "delivered"
	p.Evidence = map[string]any{"raw": "authenticated delayed delivery"}
	if err = svc.RecordProviderEvidence(ctx, p); err != nil {
		t.Fatalf("lost prior-attempt correlation: %v", err)
	}
	if err = svc.RecordProviderEvidence(ctx, p); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	history, err := svc.History(ctx, f.company, DocumentRef{Type: "estimate", ID: f.estimate})
	if err != nil || history[0].State != "accepted" {
		t.Fatalf("old evidence changed current attempt: %+v %v", history, err)
	}
	p.State = "bounced"
	if err = svc.RecordProviderEvidence(ctx, p); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed duplicate=%v", err)
	}
	p.EventID = "forged"
	p.ProviderIdentifier = "unrelated"
	if err = svc.RecordProviderEvidence(ctx, p); !errors.Is(err, ErrForbidden) {
		t.Fatalf("uncorrelated provider=%v", err)
	}
}
