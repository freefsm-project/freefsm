package delivery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRetryWindowSurvivesRestartAndExplicitManualRetry(t *testing.T) {
	for _, crash := range []bool{false, true} {
		t.Run(map[bool]string{false: "temporary_failure", true: "ambiguous_crash"}[crash], func(t *testing.T) {
			f := newDeliveryFixture(t)
			ctx := context.Background()
			svc := New(f.db.Pool, "")
			actor := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
			r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"x@y.test"}, Subject: "Frozen", TextBody: "B", PDF: []byte("pdf-v1"), PDFFilename: "x.pdf"}}
			d, err := svc.Queue(ctx, actor, r)
			if err != nil {
				t.Fatal(err)
			}
			sender := &fakeSender{err: errors.New("temporary")}
			if crash {
				_, err = svc.Claim(ctx)
			} else {
				_, err = svc.ProcessOne(ctx, sender, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			// A newly constructed worker has only durable history to consult.
			restarted := New(f.db.Pool, "")
			restarted.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
			before := sender.calls
			worked, err := restarted.ProcessOne(ctx, sender, nil)
			if err != nil {
				t.Fatal(err)
			}
			if worked || sender.calls != before {
				t.Fatal("automatic transport attempted after 24-hour window")
			}
			history, err := restarted.History(ctx, f.company, r.Document)
			if err != nil || len(history) != 1 || history[0].State != "failed" {
				t.Fatalf("expired history=%+v err=%v", history, err)
			}
			if err = restarted.ManualRetry(ctx, actor, d.ID, "reviewed expired communication"); err != nil {
				t.Fatal(err)
			}
			worked, err = restarted.ProcessOne(ctx, sender, nil)
			if err != nil || !worked || sender.calls != before+1 {
				t.Fatalf("explicit retry worked=%v sends=%d err=%v", worked, sender.calls, err)
			}
			history, err = restarted.History(ctx, f.company, r.Document)
			if err != nil || history[0].State != "failed" || history[0].LifetimeAttemptCount != 2 {
				t.Fatalf("manual retry renewed window: %+v err=%v", history, err)
			}
		})
	}
}

func TestManualRetryCannotInventUnknownOriginalWindow(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	a := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	d, err := svc.Queue(ctx, a, QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"owned@example.test"}, Subject: "Legacy", PDF: []byte("pdf-v1"), PDFFilename: "x.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	// Installed counters can outlive individual recorded attempts. This does
	// not assert when those missing attempts occurred.
	if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET lifetime_attempt_count=2,cycle_attempt_count=2 WHERE id=$1`, d.ID); err != nil {
		t.Fatal(err)
	}
	sender := &fakeSender{err: errors.New("temporary")}
	if worked, err := svc.ProcessOne(ctx, sender, nil); err != nil || worked {
		t.Fatalf("unknown original window sent automatically: %v %v", worked, err)
	}
	if err = svc.ManualRetry(ctx, a, d.ID, "reviewed original intent"); err != nil {
		t.Fatal(err)
	}
	if worked, err := svc.ProcessOne(ctx, sender, nil); err != nil || !worked {
		t.Fatalf("manual retry=%v %v", worked, err)
	}
	history, err := svc.History(ctx, f.company, DocumentRef{Type: "estimate", ID: f.estimate})
	if err != nil || history[0].State != "failed" || sender.calls != 1 {
		t.Fatalf("manual invented original window: %+v calls=%d err=%v", history, sender.calls, err)
	}
}
