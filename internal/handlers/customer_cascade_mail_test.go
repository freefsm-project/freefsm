package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestCustomerCascadeRetainedMailRetryRestoreHTTP(t *testing.T) {
	for _, retryFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("retryFirst=%v", retryFirst), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained paid and Void mail").SaveX(ctx)
			base := fmt.Sprintf("/customers/%d", c.ID)
			_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_host='localhost',smtp_from='sender@example.test' WHERE company_id=$1`, company)
			historyCheck(t, err)
			svc := delivery.New(pool, "")
			var retry creditCommand
			for _, kind := range []string{"paid", "void"} {
				fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {kind + " retained history"}, "line_items": {"[]"}}, 303)
				var id int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT max(id) FROM invoices`).Scan(&id))
				path := fmt.Sprintf("/invoices/%d", id)
				historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id))
				expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
				version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
				fieldPost(t, web, cookie, path+"/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"retained@example.test"}, "subject": {kind}, "body": {"Frozen history"}}, 303)
				if kind == "paid" {
					worked, err := svc.ProcessOne(ctx, deliveryTestSender(func(context.Context, delivery.Delivery) (delivery.SendResult, error) {
						return delivery.SendResult{}, &delivery.SendError{Kind: delivery.SendPermanent, Err: errors.New("owned permanent rejection")}
					}), nil)
					historyCheck(t, err)
					if !worked {
						t.Fatal("failed attempt missing")
					}
					var deliveryID int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM document_deliveries WHERE invoice_id=$1`, id).Scan(&deliveryID))
					retry = creditCommand{fmt.Sprintf("%s/deliveries/%d/retry", path, deliveryID), url.Values{"idempotency_key": {newOperationKey()}, "reason": {"Explicit retry before archive"}}}
				} else {
					var status int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void'`, company).Scan(&status))
					historyCheck(t, statusflow.New(pool).TransitionInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id, status))
				}
			}
			var preview authorization.CustomerArchivePreview
			masterJSON(t, web, cookie, base+"/archive", &preview)
			archive := creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}
			f := &creditMatrix{pool: pool, web: web, cookie: cookie, ctx: ctx}
			var archived, retried *httptest.ResponseRecorder
			if retryFirst {
				retried, archived = f.ordered(t, retry, archive)
			} else {
				archived, retried = f.ordered(t, archive, retry)
			}
			if archived.Code != 303 || (retryFirst && retried.Code != 303) || (!retryFirst && retried.Code < 400) {
				t.Fatalf("archive=%d retry=%d %s", archived.Code, retried.Code, retried.Body.String())
			}
			var cancelled int
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries WHERE state='cancelled' AND cancelled_at IS NOT NULL AND last_error<>''`).Scan(&cancelled))
			if cancelled != 2 {
				t.Fatalf("retained mail cancellation=%d", cancelled)
			}
			// Attempt history is retained in document_delivery_events, not a
			// separate attempt table; compare its exact recorded rows as well.
			tables := []string{"invoices", "document_deliveries", "document_delivery_events"}
			before := historyDurableRows(t, pool, tables)
			masterPost(t, web, cookie, base+"/restore", nil, 303)
			historyEqual(t, "Customer restore cannot change documents or revive cancelled mail/history", historyDurableRows(t, pool, tables), before)
			worked, err := svc.ProcessOne(ctx, deliveryTestSender(func(context.Context, delivery.Delivery) (delivery.SendResult, error) {
				t.Error("restored Customer revived delivery")
				return delivery.SendResult{}, nil
			}), nil)
			historyCheck(t, err)
			if worked {
				t.Fatal("cancelled mail claimed")
			}
			retry.values.Set("idempotency_key", newOperationKey())
			if result := f.call(retry); result.Code < 400 {
				t.Fatal("manual retry revived cancellation")
			}
		})
	}
}
