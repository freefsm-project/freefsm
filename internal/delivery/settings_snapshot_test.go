package delivery

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestSettingsChangesPreserveQueuedSnapshot(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `UPDATE company_settings SET smtp_host='old.example',smtp_from='original@example.com' WHERE company_id=$1`, f.company); err != nil {
		t.Fatal(err)
	}
	svc := New(f.db.Pool, "")
	d, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"recipient@example.com"}, Subject: "Frozen subject", TextBody: "Frozen body", PDF: []byte("original bytes"), PDFFilename: "original.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(ctx, `UPDATE company_settings SET smtp_host='new.example',smtp_from='replacement@example.com',estimate_email_subject='New template' WHERE company_id=$1`, f.company); err != nil {
		t.Fatal(err)
	}
	claimed, err := svc.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.From != "original@example.com" || claimed.Subject != "Frozen subject" || claimed.TextBody != "Frozen body" || string(claimed.PDF) != "original bytes" || claimed.MessageID != d.MessageID || claimed.LifetimeAttemptCount != 1 {
		t.Fatalf("snapshot changed: %+v", claimed)
	}
}

func TestQueueRejectsMissingTransportWithoutInventingSnapshot(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	if _, err := f.db.Pool.Exec(ctx, `UPDATE company_settings SET smtp_host='' WHERE company_id=$1`, f.company); err != nil {
		t.Fatal(err)
	}
	_, err := New(f.db.Pool, "").Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"recipient@example.com"}, Subject: "Subject", PDF: []byte("bytes"), PDFFilename: "file.pdf"}})
	if err == nil {
		t.Fatal("queued without a configured transport")
	}
}
