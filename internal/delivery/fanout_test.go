package delivery

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestQueueFanoutImmutableRecipients(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "https://example.test")
	a := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{
		To: []string{"outside@example.test", "second@example.test"}, CC: []string{"copy@example.test"}, BCC: []string{"private@example.test", "outside@example.test"},
		Subject: "Reviewed subject", TextBody: "Reviewed message", PDF: []byte("pdf-v1 exact"), PDFFilename: "reviewed.pdf",
	}}
	first, err := svc.Queue(ctx, a, r)
	if err != nil {
		t.Fatal(err)
	}
	history, err := svc.History(ctx, f.company, r.Document)
	if err != nil || len(history) != 4 {
		t.Fatalf("fanout history = %+v, %v", history, err)
	}
	seen := map[string]bool{}
	for _, row := range history {
		if len(row.To) != 1 || seen[row.To[0]] {
			t.Fatalf("not independent recipients: %+v", history)
		}
		seen[row.To[0]] = true
	}
	again, err := svc.Queue(ctx, a, r)
	if err != nil || again.ID != first.ID {
		t.Fatalf("lost response replay = %+v, %v", again, err)
	}
	var concurrent sync.WaitGroup
	for range 2 {
		concurrent.Add(1)
		go func() {
			defer concurrent.Done()
			replayed, err := svc.Queue(ctx, a, r)
			if err != nil || replayed.ID != first.ID {
				t.Errorf("concurrent replay = %d, %v", replayed.ID, err)
			}
		}()
	}
	concurrent.Wait()
	changed := r
	changed.Snapshot.To = []string{"replacement@example.test"}
	if _, err := svc.Queue(ctx, a, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed intent = %v", err)
	}
	sender := &recipientCapture{messages: map[string]Delivery{}}
	for range 4 {
		if processed, err := svc.ProcessOne(ctx, sender, nil); !processed || err != nil {
			t.Fatalf("worker = %v, %v", processed, err)
		}
	}
	if len(sender.messages) != 4 {
		t.Fatalf("messages = %+v", sender.messages)
	}
	ids := map[string]bool{}
	for recipient, d := range sender.messages {
		if !seen[recipient] || len(d.CC) != 0 || len(d.BCC) != 0 || ids[d.MessageID] || d.MessageID == "" || string(d.PDF) != "pdf-v1 exact" || d.Subject != r.Snapshot.Subject || d.TextBody != r.Snapshot.TextBody {
			t.Fatalf("snapshot = %+v", d)
		}
		ids[d.MessageID] = true
	}
	if processed, err := svc.ProcessOne(ctx, sender, nil); processed || err != nil {
		t.Fatalf("unexpected duplicate work: %v, %v", processed, err)
	}
}

type recipientCapture struct{ messages map[string]Delivery }

func (s *recipientCapture) Send(_ context.Context, d Delivery) (SendResult, error) {
	s.messages[d.To[0]] = d
	return SendResult{Evidence: map[string]any{"accepted": true}}, nil
}

func TestQueueRejectsInvalidRecipientWithoutPartialFanout(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"valid@example.test"}, BCC: []string{"not-an-address"}, Subject: "Reviewed", TextBody: "Body", PDF: []byte("pdf-v1"), PDFFilename: "estimate.pdf"}}
	if _, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid recipient accepted: %v", err)
	}
	history, err := svc.History(ctx, f.company, r.Document)
	if err != nil || len(history) != 0 {
		t.Fatalf("partial fanout = %+v, %v", history, err)
	}
}

func TestQueueFanoutFailureRollsBackWholeRequest(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	// Inject a storage failure on the second recipient, after the first insert
	// and its events have executed. Retrying the same intent must queue both.
	_, err := f.db.Pool.Exec(ctx, `CREATE FUNCTION fail_second_recipient() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.recipients_to=ARRAY['z@example.test'] THEN RAISE EXCEPTION 'injected fanout failure'; END IF; RETURN NEW; END $$;
	CREATE TRIGGER fanout_fault BEFORE INSERT ON document_deliveries FOR EACH ROW EXECUTE FUNCTION fail_second_recipient()`)
	if err != nil {
		t.Fatal(err)
	}
	r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"a@example.test", "z@example.test"}, Subject: "Reviewed", TextBody: "Body", PDF: []byte("pdf-v1"), PDFFilename: "estimate.pdf"}}
	a := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	if _, err := svc.Queue(ctx, a, r); err == nil {
		t.Fatal("storage failure ignored")
	}
	history, err := svc.History(ctx, f.company, r.Document)
	if err != nil || len(history) != 0 {
		t.Fatalf("partial fanout = %+v, %v", history, err)
	}
	if _, err := f.db.Pool.Exec(ctx, `DROP TRIGGER fanout_fault ON document_deliveries`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Queue(ctx, a, r); err != nil {
		t.Fatal(err)
	}
	history, err = svc.History(ctx, f.company, r.Document)
	if err != nil || len(history) != 2 {
		t.Fatalf("retry = %+v, %v", history, err)
	}
}
