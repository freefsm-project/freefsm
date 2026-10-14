package statusflow

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type integrationDeliveryFixture struct {
	db            *pgxpool.Pool
	svc           *Service
	outbox        *delivery.Service
	a             Actor
	d             delivery.Delivery
	sent, initial int64
}

func integrationDelivery(t *testing.T, typ string) integrationDeliveryFixture {
	t.Helper()
	db, company, admin, _, customer := statusDB(t)
	ctx := context.Background()
	var workflow, initial, sent, document int64
	mustScan(t, db.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,$2,$2) RETURNING id`, company, typ), &workflow)
	categories := []string{"draft", "estimate", "sent", "accepted", "rejected", "completed"}
	initialCategory := "draft"
	if typ == "invoice" {
		categories = []string{"draft", "invoiced", "sent", "partially_paid", "paid", "void"}
		initialCategory = "invoiced"
	}
	withTx(t, db, func(tx pgx.Tx) error {
		for _, category := range categories {
			if _, err := tx.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES($1,$2,$3,$4,1,true)`, company, workflow, category, typ+":"+category); err != nil {
				return err
			}
		}
		return nil
	})
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key=$2`, workflow, typ+":"+initialCategory), &initial)
	mustScan(t, db.QueryRow(ctx, `SELECT id FROM statuses WHERE workflow_id=$1 AND category_key=$2`, workflow, typ+":sent"), &sent)
	mustScan(t, db.QueryRow(ctx, fmt.Sprintf(`INSERT INTO %ss(company_id,customer_id,status_id,title) VALUES($1,$2,$3,'Delivery') RETURNING id`, typ), company, customer, initial), &document)
	if _, err := db.Exec(ctx, `UPDATE company_settings SET smtp_host='localhost',smtp_from='frozen@example.com' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	outbox := delivery.New(db, "")
	d, err := outbox.Queue(ctx, delivery.Actor{ID: admin, CompanyID: company, Role: "admin"}, delivery.QueueRequest{Key: uuid.New(), Document: delivery.DocumentRef{Type: typ, ID: document}, Snapshot: delivery.Snapshot{To: []string{"customer@example.com"}, Subject: "Frozen", PDF: []byte("selected PDF"), PDFFilename: "selected.pdf"}})
	if err != nil {
		t.Fatal(err)
	}
	return integrationDeliveryFixture{db, New(db), outbox, Actor{ID: admin, CompanyID: company}, d, sent, initial}
}

type integrationSender func(context.Context, delivery.Delivery) (delivery.SendResult, error)

func (s integrationSender) Send(ctx context.Context, d delivery.Delivery) (delivery.SendResult, error) {
	return s(ctx, d)
}

func integrationSend(_ context.Context, d delivery.Delivery) (delivery.SendResult, error) {
	if d.From != "frozen@example.com" || string(d.PDF) != "selected PDF" || d.PDFFilename != "selected.pdf" {
		return delivery.SendResult{}, fmt.Errorf("frozen snapshot changed")
	}
	return delivery.SendResult{ProviderIdentifier: "smtp-accepted"}, nil
}

func TestIntegrationDeliveryWorkflowRevisions(t *testing.T) {
	for _, typ := range []string{"estimate", "invoice"} {
		for _, change := range []string{"unchanged", "rename", "record ABA", "meaning ABA", "legacy unknown"} {
			t.Run(typ+"/"+change, func(t *testing.T) {
				f := integrationDelivery(t, typ)
				ctx := context.Background()
				switch change {
				case "rename":
					if _, err := f.svc.Update(ctx, f.a, f.initial, "Renamed", "#112233"); err != nil {
						t.Fatal(err)
					}
				case "record ABA":
					withTx(t, f.db, func(tx pgx.Tx) error {
						if err := allowStatusUpdates(ctx, tx); err != nil {
							return err
						}
						for _, status := range []int64{f.sent, f.initial} {
							if _, err := tx.Exec(ctx, `UPDATE `+typ+`s SET status_id=$1 WHERE id=$2`, status, f.d.DocumentID); err != nil {
								return err
							}
						}
						return nil
					})
				case "meaning ABA":
					// Exercise the real meaning trigger, keeping each category valid
					// at commit by swapping back within the same transaction.
					withTx(t, f.db, func(tx pgx.Tx) error {
						if err := allowStatusUpdates(ctx, tx); err != nil {
							return err
						}
						var category string
						if err := tx.QueryRow(ctx, `SELECT category_key FROM statuses WHERE id=$1`, f.initial).Scan(&category); err != nil {
							return err
						}
						if _, err := tx.Exec(ctx, `UPDATE statuses SET category_key=$1,is_category_default=false,category_order=100 WHERE id=$2`, typ+":sent", f.initial); err != nil {
							return err
						}
						_, err := tx.Exec(ctx, `UPDATE statuses SET category_key=$1,is_category_default=true,category_order=1 WHERE id=$2`, category, f.initial)
						return err
					})
				case "legacy unknown":
					// Import a pre-migration snapshot, omitting the nullable evidence.
					// Do not weaken the immutable-intent update trigger to simulate it.
					_, err := f.db.Exec(ctx, `INSERT INTO document_deliveries(company_id,document_type,document_id,estimate_id,invoice_id,actor_id,idempotency_key,request_fingerprint,recipients_to,subject,text_body,pdf_data,pdf_filename,message_id,expected_status_id,sender_address)
					 SELECT company_id,document_type,document_id,estimate_id,invoice_id,actor_id,gen_random_uuid(),request_fingerprint,recipients_to,subject,text_body,pdf_data,pdf_filename,message_id||'.legacy',expected_status_id,sender_address FROM document_deliveries WHERE id=$1`, f.d.ID)
					if err != nil {
						t.Fatal(err)
					}
					// Consume the modern row without applying automation first.
					if _, err = f.outbox.ProcessOne(ctx, integrationSender(integrationSend), nil); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.db.Exec(ctx, `UPDATE company_settings SET smtp_from='changed@example.com' WHERE company_id=$1`, f.a.CompanyID); err != nil {
					t.Fatal(err)
				}
				if ok, err := f.outbox.ProcessOne(ctx, integrationSender(integrationSend), NewAcceptanceHook(f.svc)); err != nil || !ok {
					t.Fatalf("acceptance: %v %v", ok, err)
				}
				var actual int64
				var pending, transitions int
				mustScan(t, f.db.QueryRow(ctx, `SELECT status_id FROM `+typ+`s WHERE id=$1`, f.d.DocumentID), &actual)
				mustScan(t, f.db.QueryRow(ctx, `SELECT count(*) FROM document_deliveries WHERE state<>'accepted'`), &pending)
				mustScan(t, f.db.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE action='status_transitioned'`), &transitions)
				want, logs := f.initial, 0
				if change == "unchanged" || change == "rename" {
					want, logs = f.sent, 1
				}
				if actual != want || pending != 0 || transitions != logs {
					t.Fatalf("status=%d want=%d pending=%d transitions=%d want=%d", actual, want, pending, transitions, logs)
				}
			})
		}
	}
}

func TestIntegrationDeliverySMTPAllowsDestinationReplacement(t *testing.T) {
	f := integrationDelivery(t, "estimate")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	replacement, err := f.svc.Create(ctx, f.a, CreateRequest{Type: Estimate, Name: "New sent", Category: EstimateSent})
	if err != nil {
		t.Fatal(err)
	}
	sending, resume := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := f.outbox.ProcessOne(ctx, integrationSender(func(ctx context.Context, d delivery.Delivery) (delivery.SendResult, error) {
			close(sending)
			select {
			case <-resume:
				return integrationSend(ctx, d)
			case <-ctx.Done():
				return delivery.SendResult{}, ctx.Err()
			}
		}), NewAcceptanceHook(f.svc))
		done <- err
	}()
	select {
	case <-sending:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Must finish while SMTP is blocked: no database locks span Send.
	err = f.svc.Delete(ctx, f.a, f.sent, replacement.ID)
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	var actual int64
	mustScan(t, f.db.QueryRow(ctx, `SELECT status_id FROM estimates WHERE id=$1`, f.d.DocumentID), &actual)
	if actual != replacement.ID {
		t.Fatalf("acceptance used deleted destination: %d", actual)
	}
}

func TestIntegrationDeliveryAcceptanceFencePrecedesRows(t *testing.T) {
	f := integrationDelivery(t, "estimate")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Other test schemas share this database-wide fence. Observe this worker,
	// not an unrelated package waiting while this worker is still claiming a row.
	config := f.db.Config()
	workerName := "delivery-fence-" + uuid.NewString()
	config.ConnConfig.RuntimeParams["application_name"] = workerName
	workerPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var blocker int
	mustScan(t, tx.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`), &blocker)
	// This is the configuration lock order: fence, then status/document.
	if _, err = tx.Exec(ctx, `SELECT id FROM statuses WHERE id=$1 FOR UPDATE`, f.initial); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := delivery.New(workerPool, "").ProcessOne(ctx, integrationSender(integrationSend), NewAcceptanceHook(f.svc))
		done <- err
	}()
	for {
		var waiting bool
		mustScan(t, f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%pg_advisory_xact_lock(190019)%')`, blocker, workerName), &waiting)
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("worker did not wait for fence: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	// Acceptance must not hold even the outbox row while waiting on the fence.
	if _, err = tx.Exec(ctx, `SELECT id FROM document_deliveries WHERE id=$1 FOR UPDATE NOWAIT`, f.d.ID); err != nil {
		t.Fatalf("acceptance locked row before fence: %v", err)
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM estimates WHERE id=$1 FOR UPDATE NOWAIT`, f.d.DocumentID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
