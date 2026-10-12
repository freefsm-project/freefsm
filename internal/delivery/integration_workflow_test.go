package delivery

import (
	"context"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type retryAcceptanceHook struct{ calls int }

func (h *retryAcceptanceHook) OnAccepted(ctx context.Context, tx pgx.Tx, _ Delivery) error {
	h.calls++
	if h.calls == 1 {
		// A real PostgreSQL transaction abort, after acceptance/event writes.
		_, err := tx.Exec(ctx, `DO $$ BEGIN RAISE EXCEPTION 'retry acceptance' USING ERRCODE='40001'; END $$`)
		return err
	}
	return nil
}

func TestIntegrationWorkflowIntentPreservesSelectedPDF(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := authorization.WithActor(context.Background(), authorization.New(f.db.Pool), authorization.Actor{UserID: f.admin, Channel: "web"})
	var version int64
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO document_pdf_versions(company_id,document_type,document_id,source_token,filename,content,generated_by,source_snapshot) VALUES($1,'estimate',$2,'historical-source','selected.pdf',$3,$4,'{}') RETURNING id`, f.company, f.estimate, []byte("selected immutable bytes"), f.admin).Scan(&version); err != nil {
		t.Fatal(err)
	}
	svc := New(f.db.Pool, "")
	a := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, PDFVersionID: version, ConfirmStalePDF: true, Snapshot: Snapshot{To: []string{"recipient@example.com"}, Subject: "Frozen", PDF: []byte("untrusted caller bytes"), PDFFilename: "untrusted.pdf"}}
	d, err := svc.Queue(ctx, a, r)
	if err != nil {
		t.Fatal(err)
	}
	if string(d.PDF) != "selected immutable bytes" || d.PDFFilename != "selected.pdf" || d.ExpectedStatusRevision == nil || d.ExpectedMeaningRevision == nil {
		t.Fatalf("selected snapshot or workflow intent lost: %+v", d)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE estimates SET status_revision=gen_random_uuid() WHERE id=$1`, f.estimate); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE company_settings SET smtp_from='new@example.com' WHERE company_id=$1`, f.company); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.Queue(ctx, a, r)
	if err != nil || replay.ID != d.ID || replay.ExpectedStatusRevision == nil || *replay.ExpectedStatusRevision != *d.ExpectedStatusRevision {
		t.Fatalf("selected-PDF replay changed intent: %+v %v", replay, err)
	}
	claimed, err := svc.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var selected int64
	if err = f.db.Pool.QueryRow(ctx, `SELECT pdf_version_id FROM document_deliveries WHERE id=$1`, d.ID).Scan(&selected); err != nil {
		t.Fatal(err)
	}
	if selected != version || claimed.From != "sender@example.com" || string(claimed.PDF) != "selected immutable bytes" || claimed.PDFFilename != "selected.pdf" || claimed.MessageID != d.MessageID {
		t.Fatalf("claimed selection changed: version=%d delivery=%+v", selected, claimed)
	}
}

func TestIntegrationAcceptancePersistenceRetryAndFrozenIntent(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	svc := New(f.db.Pool, "")
	r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"recipient@example.com"}, Subject: "Frozen", PDF: []byte("pdf-v1"), PDFFilename: "frozen.pdf"}}
	a := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
	d, err := svc.Queue(ctx, a, r)
	if err != nil {
		t.Fatal(err)
	}
	var record, meaning uuid.UUID
	if err = f.db.Pool.QueryRow(ctx, `SELECT o.status_revision,s.meaning_revision FROM estimates o JOIN statuses s ON s.id=o.status_id WHERE o.id=$1`, f.estimate).Scan(&record, &meaning); err != nil {
		t.Fatal(err)
	}
	if d.ExpectedStatusRevision == nil || d.ExpectedMeaningRevision == nil || *d.ExpectedStatusRevision != record || *d.ExpectedMeaningRevision != meaning {
		t.Fatalf("queue did not capture current workflow revisions: %+v", d)
	}
	for _, column := range []string{"expected_status_revision", "expected_meaning_revision"} {
		if _, err = f.db.Pool.Exec(ctx, `UPDATE document_deliveries SET `+column+`=gen_random_uuid() WHERE id=$1`, d.ID); err == nil {
			t.Fatalf("%s was mutable", column)
		}
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE estimates SET status_revision=gen_random_uuid() WHERE id=$1`, f.estimate); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.Queue(ctx, a, r)
	if err != nil || replay.ID != d.ID || replay.ExpectedStatusRevision == nil || *replay.ExpectedStatusRevision != record {
		t.Fatalf("replay recaptured intent: %+v %v", replay, err)
	}
	sender, hook := &fakeSender{}, &retryAcceptanceHook{}
	if ok, err := svc.ProcessOne(ctx, sender, hook); err != nil || !ok {
		t.Fatalf("processing: %v %v", ok, err)
	}
	if sender.calls != 1 || hook.calls != 2 {
		t.Fatalf("sends=%d persistence attempts=%d", sender.calls, hook.calls)
	}
	var state string
	var attempts, events, logs int
	if err = f.db.Pool.QueryRow(ctx, `SELECT state,lifetime_attempt_count FROM document_deliveries WHERE id=$1`, d.ID).Scan(&state, &attempts); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM document_delivery_events WHERE delivery_id=$1 AND to_state='accepted'`, d.ID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE action='document_delivery_accepted' AND object_id=$1`, f.estimate).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if state != "accepted" || attempts != 1 || events != 1 || logs != 1 {
		t.Fatalf("state=%s attempts=%d events=%d logs=%d", state, attempts, events, logs)
	}
}
