package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestDeliveryArchiveDispatchBoundaryHTTP(t *testing.T) {
	for _, customerArchive := range []bool{false, true} {
		for _, transportFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("customer_%v/dispatch_first_%v", customerArchive, transportFirst), func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cookie := sessionCookie(t, ctx, sessions, admin)
				customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained Invoice customer").SaveX(ctx)
				fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Retained Invoice"}, "invoice_date": {"2026-01-02"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
				var id int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE customer_id=$1`, customer.ID).Scan(&id))
				historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id))
				_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
				historyCheck(t, err)
				path := fmt.Sprintf("/invoices/%d", id)
				expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
				version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
				message := url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"one@example.test,two@example.test"}, "subject": {"Review"}, "body": {"Message"}}
				fieldPost(t, web, cookie, path+"/email", message, 303)
				archive := creditCommand{path + "/delete", url.Values{}}
				event := "UPDATE OF deleted_at ON invoices"
				if customerArchive {
					base := fmt.Sprintf("/customers/%d", customer.ID)
					var preview struct{ Snapshot string }
					masterJSON(t, web, cookie, base+"/archive", &preview)
					archive = creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}
					event = "UPDATE OF deleted_at ON customers"
				}
				when := ""
				if !customerArchive && !transportFirst {
					before := historyDurableRows(t, pool, []string{"document_deliveries", "document_delivery_events"})
					_, err = pool.Exec(ctx, `CREATE FUNCTION fail_archive_delivery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.to_state='cancelled' AND (SELECT count(*) FROM document_deliveries WHERE state='cancelled')=2 THEN RAISE EXCEPTION 'injected second-recipient cancellation audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_archive_delivery_audit BEFORE INSERT ON document_delivery_events FOR EACH ROW EXECUTE FUNCTION fail_archive_delivery_audit()`)
					historyCheck(t, err)
					failed := (&creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}).call(archive)
					if failed.Code != 500 {
						t.Fatalf("archive audit failure=%d %s", failed.Code, failed.Body.String())
					}
					historyEqual(t, "archive audit failure rolls back all cancellations", historyDurableRows(t, pool, []string{"document_deliveries", "document_delivery_events"}), before)
					var archived bool
					historyCheck(t, pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM invoices WHERE id=$1`, id).Scan(&archived))
					if archived {
						t.Fatal("cancellation audit failure committed document archive")
					}
					_, err = pool.Exec(ctx, `DROP TRIGGER fail_archive_delivery_audit ON document_delivery_events`)
					historyCheck(t, err)
				}
				if transportFirst {
					event = "UPDATE OF handoff_started_at ON document_deliveries"
					when = " WHEN (NEW.handoff_started_at IS NOT NULL)"
				}
				_, err = pool.Exec(ctx, `CREATE FUNCTION delivery_handoff_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(560056); RETURN NEW; END $$; CREATE TRIGGER delivery_handoff_barrier BEFORE `+event+` FOR EACH ROW`+when+` EXECUTE FUNCTION delivery_handoff_barrier()`)
				historyCheck(t, err)
				barrier, err := pool.Begin(ctx)
				historyCheck(t, err)
				defer barrier.Rollback(context.Background())
				var holder, firstPID int
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(560056)`).Scan(&holder))
				archived := make(chan *httptest.ResponseRecorder, 1)
				type result struct {
					worked bool
					err    error
				}
				sent := make(chan result, 1)
				f := &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}
				sender := &compositionSender{}
				svc := delivery.New(pool, "")
				runArchive := func() { archived <- f.call(archive) }
				runWorker := func() { worked, err := svc.ProcessOne(ctx, sender, nil); sent <- result{worked, err} }
				if transportFirst {
					go runWorker()
				} else {
					go runArchive()
				}
				commercialWaiters(t, ctx, pool, holder, 1)
				historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE application_name=$2 AND $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, holder, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&firstPID))
				if transportFirst {
					go runArchive()
				} else {
					go runWorker()
				}
				commercialWaiters(t, ctx, pool, firstPID, 1)
				t.Logf("observed archive/dispatch database barrier holder=%d first=%d", holder, firstPID)
				historyCheck(t, barrier.Commit(ctx))
				response := <-archived
				worker := <-sent
				if response.Code != 303 || worker.err != nil || worker.worked != transportFirst {
					t.Fatalf("archive=%d %s worker=%+v", response.Code, response.Body.String(), worker)
				}
				if len(sender.messages) != boolInt(transportFirst) {
					t.Fatalf("transport calls=%d", len(sender.messages))
				}
				history, err := svc.History(ctx, company, delivery.DocumentRef{Type: "invoice", ID: id})
				historyCheck(t, err)
				accepted, cancelled := 0, 0
				for _, row := range history {
					if row.State == "accepted" {
						accepted++
					}
					if row.State == "cancelled" {
						cancelled++
					}
					if row.DeliveredAt != nil {
						t.Fatal("SMTP acceptance fabricated delivery evidence")
					}
				}
				if accepted != boolInt(transportFirst) || cancelled != 2-accepted {
					t.Fatalf("history=%+v", history)
				}
				message.Set("idempotency_key", newOperationKey())
				fieldPost(t, web, cookie, path+"/email", message, 403)
			})
		}
	}
}

func TestDeliveryCustomerArchiveQueueBoundaryHTTP(t *testing.T) {
	for _, queueFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(queueFirst), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained paid Invoice").SaveX(ctx)
			fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Retained Invoice"}, "line_items": {"[]"}}, 303)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE customer_id=$1`, customer.ID).Scan(&id))
			historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id))
			_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
			historyCheck(t, err)
			path := fmt.Sprintf("/invoices/%d", id)
			expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
			version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
			base := fmt.Sprintf("/customers/%d", customer.ID)
			var preview struct{ Snapshot string }
			masterJSON(t, web, cookie, base+"/archive", &preview)
			queue := creditCommand{path + "/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"one@example.test,two@example.test"}, "subject": {"Review"}, "body": {"Message"}}}
			archive := creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}
			first, second, event := queue, archive, "INSERT ON document_deliveries"
			if !queueFirst {
				first, second, event = archive, queue, "UPDATE OF deleted_at ON customers"
			}
			a, b := conversionOrderedEffects(t, &creditMatrix{ctx: ctx, pool: pool, web: web, cookie: cookie}, first, second, event)
			queued, archived := a, b
			if !queueFirst {
				queued, archived = b, a
			}
			want := 403
			if queueFirst {
				want = 303
			}
			if queued.Code != want || archived.Code != 303 {
				t.Fatalf("queue=%d %s archive=%d %s", queued.Code, queued.Body.String(), archived.Code, archived.Body.String())
			}
			history, err := delivery.New(pool, "").History(ctx, company, delivery.DocumentRef{Type: "invoice", ID: id})
			historyCheck(t, err)
			if len(history) != 2*boolInt(queueFirst) {
				t.Fatal(history)
			}
			for _, row := range history {
				if row.State != "cancelled" {
					t.Fatal("Customer archive left sendable recipient")
				}
			}
		})
	}
}

func TestDeliveryInvoiceReversionCancelsWithoutRetargetingHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Conversion Customer").SaveX(ctx)
	fieldPost(t, web, cookie, "/estimates", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Proposal"}, "line_items": {"[]"}}, 303)
	var estimate int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&estimate))
	convert := fmt.Sprintf("/estimates/%d/convert", estimate)
	fieldPost(t, web, cookie, convert, conversionReview(t, web, cookie, convert), 303)
	var invoice int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, estimate).Scan(&invoice))
	path := fmt.Sprintf("/invoices/%d", invoice)
	conversionUpload(t, web, cookie, "invoice", invoice, "retained.txt", "exact original File")
	var file int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM files`).Scan(&file))
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
	historyCheck(t, err)
	expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
	message := url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"one@example.test"}, "subject": {"Review"}, "body": {"Message"}, "extra_file_id": {fmt.Sprint(file)}}
	fieldPost(t, web, cookie, path+"/email", message, 303)
	revert := path + "/revert-to-estimate"
	fieldPost(t, web, cookie, revert, conversionReview(t, web, cookie, revert), 303)
	message.Set("idempotency_key", newOperationKey())
	fieldPost(t, web, cookie, path+"/email", message, 403)
	svc := delivery.New(pool, "")
	history, err := svc.History(ctx, company, delivery.DocumentRef{Type: "invoice", ID: invoice})
	historyCheck(t, err)
	if len(history) != 1 || history[0].State != "cancelled" {
		t.Fatal(history)
	}
	newer, err := svc.History(ctx, company, delivery.DocumentRef{Type: "estimate", ID: estimate})
	historyCheck(t, err)
	if len(newer) != 0 {
		t.Fatal("hidden Invoice send followed conversion parent")
	}
	sender := &compositionSender{}
	worked, err := svc.ProcessOne(ctx, sender, nil)
	historyCheck(t, err)
	if worked || len(sender.messages) != 0 {
		t.Fatal("hidden original document sent")
	}
}
