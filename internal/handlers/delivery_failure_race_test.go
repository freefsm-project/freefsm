package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestDeliveryFailureCannotRequeueBehindArchiveCancellation(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Failure race").SaveX(ctx)
	fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Failure race"}, "line_items": {"[]"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE customer_id=$1`, customer.ID).Scan(&id))
	historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id))
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='localhost',smtp_from='sender@example.test' WHERE company_id=$1`, company)
	historyCheck(t, err)
	path := fmt.Sprintf("/invoices/%d", id)
	expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
	fieldPost(t, web, cookie, path+"/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"recipient@example.test"}, "subject": {"Frozen"}, "body": {"Message"}}, 303)
	// AFTER archive's cancellation scan, but before its commit: an admitted
	// transport has not yet finished. Its retry decision must wait for archive.
	_, err = pool.Exec(ctx, `CREATE FUNCTION hold_archive_after_cancellation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(560057); RETURN NEW; END $$; CREATE TRIGGER zz_hold_archive_after_cancellation AFTER UPDATE OF deleted_at ON invoices FOR EACH ROW EXECUTE FUNCTION hold_archive_after_cancellation()`)
	historyCheck(t, err)
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(context.Background())
	var holder, archivePID int
	historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(560057)`).Scan(&holder))
	entered, release := make(chan struct{}), make(chan struct{})
	workerDone := make(chan error, 1)
	svc := delivery.New(pool, "")
	go func() {
		_, err := svc.ProcessOne(ctx, deliveryTestSender(func(context.Context, delivery.Delivery) (delivery.SendResult, error) {
			close(entered)
			select {
			case <-release:
				return delivery.SendResult{}, errors.New("owned temporary transport failure")
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
	archived := make(chan *httptest.ResponseRecorder, 1)
	f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
	go func() { archived <- f.call(creditCommand{path + "/delete", url.Values{}}) }()
	commercialWaiters(t, ctx, pool, holder, 1)
	historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid))`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&archivePID))
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case early := <-workerDone:
			historyCheck(t, barrier.Commit(ctx))
			<-archived
			t.Fatalf("retry decision escaped uncommitted archive fence: %v", early)
		default:
		}
		var blocked bool
		historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, archivePID).Scan(&blocked))
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry did not reach archive fence")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("observed admitted-transport failure blocked behind archive completion pid=%d", archivePID)
	historyCheck(t, barrier.Commit(ctx))
	historyCheck(t, <-workerDone)
	response := <-archived
	if response.Code != 303 {
		t.Fatalf("archive=%d %s", response.Code, response.Body.String())
	}
	history, err := svc.History(ctx, company, delivery.DocumentRef{Type: "invoice", ID: id})
	historyCheck(t, err)
	if len(history) != 1 || history[0].State != "cancelled" || history[0].AcceptedAt != nil || history[0].DeliveredAt != nil {
		t.Fatalf("archive left a retry or fabricated transport acceptance: %+v", history)
	}
}
