package delivery

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

type senderFunc func(context.Context, Delivery) (SendResult, error)

func (f senderFunc) Send(ctx context.Context, d Delivery) (SendResult, error) { return f(ctx, d) }

func TestArchiveCancelsPendingAndClaimedDelivery(t *testing.T) {
	for _, target := range []string{"document", "customer"} {
		for _, claim := range []bool{false, true} {
			t.Run(target+map[bool]string{false: "/queued", true: "/claimed"}[claim], func(t *testing.T) {
				f := newDeliveryFixture(t)
				ctx := context.Background()
				svc := New(f.db.Pool, "")
				a := Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}
				r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"one@example.test", "two@example.test"}, Subject: "Frozen", TextBody: "Body", PDF: []byte("pdf-v1"), PDFFilename: "original.pdf"}}
				d, err := svc.Queue(ctx, a, r)
				if err != nil {
					t.Fatal(err)
				}
				if claim {
					if _, err := svc.Claim(ctx); err != nil {
						t.Fatal(err)
					}
				}
				sql := `UPDATE estimates SET deleted_at=now() WHERE id=$1`
				if target == "customer" {
					sql = `UPDATE customers SET deleted_at=now() WHERE id=(SELECT customer_id FROM estimates WHERE id=$1)`
				}
				if _, err := f.db.Pool.Exec(ctx, sql, f.estimate); err != nil {
					t.Fatal(err)
				}
				history, err := svc.History(ctx, f.company, r.Document)
				if err != nil || len(history) != 2 {
					t.Fatalf("history=%+v %v", history, err)
				}
				for _, row := range history {
					if row.State != "cancelled" || row.LastError == "" {
						t.Fatalf("archive left sendable work: %+v", row)
					}
				}
				if worked, err := svc.ProcessOne(ctx, senderFunc(func(context.Context, Delivery) (SendResult, error) {
					t.Error("archived mail dispatched")
					return SendResult{}, nil
				}), nil); worked || err != nil {
					t.Fatalf("worker=%v %v", worked, err)
				}
				if _, err := f.db.Pool.Exec(ctx, `UPDATE estimates SET deleted_at=NULL WHERE id=$1`, f.estimate); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Pool.Exec(ctx, `UPDATE customers SET deleted_at=NULL WHERE id=(SELECT customer_id FROM estimates WHERE id=$1)`, f.estimate); err != nil {
					t.Fatal(err)
				}
				if err := svc.ManualRetry(ctx, a, d.ID, "restore must not restart cancelled mail", uuid.New()); err == nil {
					t.Fatal("cancelled mail retried")
				}
			})
		}
	}
}

func TestArchiveDuringAdmittedTransportRetainsTruthfulOutcome(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(map[bool]string{true: "accepted", false: "failed"}[accepted], func(t *testing.T) {
			f := newDeliveryFixture(t)
			ctx := context.Background()
			svc := New(f.db.Pool, "")
			r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"one@example.test"}, Subject: "Frozen", TextBody: "Body", PDF: []byte("pdf-v1"), PDFFilename: "original.pdf"}}
			if _, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, r); err != nil {
				t.Fatal(err)
			}
			worked, err := svc.ProcessOne(ctx, senderFunc(func(ctx context.Context, d Delivery) (SendResult, error) {
				if _, err := f.db.Pool.Exec(ctx, `UPDATE estimates SET deleted_at=now() WHERE id=$1`, f.estimate); err != nil {
					return SendResult{}, err
				}
				if !accepted {
					return SendResult{}, errors.New("temporary transport failure")
				}
				return SendResult{Evidence: map[string]any{"smtp": "accepted"}}, nil
			}), nil)
			if !worked || err != nil {
				t.Fatalf("worker=%v %v", worked, err)
			}
			history, err := svc.History(ctx, f.company, r.Document)
			if err != nil {
				t.Fatal(err)
			}
			want := "cancelled"
			if accepted {
				want = "accepted"
			}
			if history[0].State != want || history[0].DeliveredAt != nil {
				t.Fatalf("untruthful outcome: %+v", history)
			}
		})
	}
}

func TestJobAndProjectArchiveDoNotCancelActiveDocumentMail(t *testing.T) {
	for _, kind := range []string{"job", "project"} {
		t.Run(kind, func(t *testing.T) {
			f := newDeliveryFixture(t)
			ctx := context.Background()
			svc := New(f.db.Pool, "")
			r := QueueRequest{Key: uuid.New(), Document: DocumentRef{Type: "estimate", ID: f.estimate}, Snapshot: Snapshot{To: []string{"one@example.test", "two@example.test"}, Subject: "Frozen", TextBody: "Body", PDF: []byte("pdf-v1"), PDFFilename: "original.pdf"}}
			if _, err := svc.Queue(ctx, Actor{ID: f.admin, CompanyID: f.company, Role: "admin"}, r); err != nil {
				t.Fatal(err)
			}
			if kind == "job" {
				if _, err := f.db.Pool.Exec(ctx, `UPDATE jobs SET deleted_at=now() WHERE id=$1`, f.job); err != nil {
					t.Fatal(err)
				}
			} else {
				var project int64
				if err := f.db.Pool.QueryRow(ctx, `INSERT INTO projects(company_id,customer_id,name) SELECT company_id,customer_id,'Archived grouping' FROM jobs WHERE id=$1 RETURNING id`, f.job).Scan(&project); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Pool.Exec(ctx, `UPDATE jobs SET project_id=$1 WHERE id=$2`, project, f.job); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Pool.Exec(ctx, `UPDATE projects SET deleted_at=now() WHERE id=$1`, project); err != nil {
					t.Fatal(err)
				}
			}
			sender := &fakeSender{}
			for range 2 {
				worked, err := svc.ProcessOne(ctx, sender, nil)
				if !worked || err != nil {
					t.Fatalf("active document blocked by %s archive: %v %v", kind, worked, err)
				}
			}
			if sender.calls != 2 {
				t.Fatal("pending recipients lost")
			}
		})
	}
}
