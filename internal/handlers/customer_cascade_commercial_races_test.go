package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestCustomerCascadeCommercialWriterCommitOrders(t *testing.T) {
	for _, mode := range []string{"invoice-ent", "invoice-pool", "estimate", "invoice-money", "http-title"} {
		for _, archiveFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/archiveFirst=%v", mode, archiveFirst), func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cookie := sessionCookie(t, ctx, sessions, admin)
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Commercial write race").SaveX(ctx)
				base := fmt.Sprintf("/customers/%d", c.ID)
				var id int64
				var values url.Values
				if mode == "invoice-money" || mode == "http-title" {
					fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Original"}, "line_items": {"[]"}}, 303)
					historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id))
					historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id))
					var number int64
					historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_number FROM invoices WHERE id=$1`, id).Scan(&number))
					values = commercialReviewedValues(t, web, cookie, fmt.Sprintf("/invoices/%d", id), url.Values{"customer_id": {fmt.Sprint(c.ID)}, "invoice_number": {fmt.Sprint(number)}, "title": {"Accepted content"}})
				}
				f := &creditMatrix{pool: pool, web: web, cookie: cookie, ctx: ctx}
				write := func() error {
					switch mode {
					case "invoice-ent", "invoice-pool":
						s := services.NewInvoiceService(client)
						if mode == "invoice-pool" {
							s = services.NewInvoiceService(client, pool)
						}
						_, err := s.Create(ctx, services.InvoiceCreateParams{CustomerID: c.ID, Title: "Racing Draft", InvoiceDate: time.Now(), DueDate: time.Now(), TaxRate: "0"})
						return err
					case "estimate":
						_, err := services.NewEstimateService(client).Create(ctx, services.EstimateCreateParams{CustomerID: c.ID, Title: "Racing proposal", TaxRate: "0"})
						return err
					case "invoice-money":
						lines := []services.LineItem{{Title: "New debt", Quantity: 1, UnitPrice: 100}}
						_, err := services.NewInvoiceService(client).Update(ctx, id, services.InvoiceUpdateParams{LineItems: &lines})
						return err
					default:
						r := f.call(creditCommand{fmt.Sprintf("/invoices/%d", id), values})
						if r.Code != 303 {
							return fmt.Errorf("HTTP details=%d: %s", r.Code, r.Body.String())
						}
						return nil
					}
				}
				var review authorization.CustomerArchivePreview
				masterJSON(t, web, cookie, base+"/archive", &review)
				before := cascadeCommercialFacts(t, pool)
				barrier, err := pool.Begin(ctx)
				historyCheck(t, err)
				defer barrier.Rollback(context.Background())
				var holder, first int
				historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&holder))
				written := make(chan error, 1)
				archived := make(chan *httptest.ResponseRecorder, 1)
				runWrite := func() { written <- write() }
				runArchive := func() {
					archived <- f.call(creditCommand{base + "/delete", url.Values{"archive_snapshot": {review.Snapshot}}})
				}
				if archiveFirst {
					go runArchive()
				} else {
					go runWrite()
				}
				commercialWaiters(t, ctx, pool, holder, 1)
				historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'`, holder).Scan(&first))
				if archiveFirst {
					go runWrite()
				} else {
					go runArchive()
				}
				commercialWaiters(t, ctx, pool, first, 1)
				t.Logf("observed commercial/archive holder=%d first=%d mode=%s archiveFirst=%v", holder, first, mode, archiveFirst)
				historyCheck(t, barrier.Commit(ctx))
				writeErr, archive := <-written, <-archived
				wantArchive := 409
				if archiveFirst || mode == "http-title" {
					wantArchive = 303
				}
				if archive.Code != wantArchive || (writeErr != nil) != archiveFirst {
					t.Fatalf("write=%v archive=%d want %d: %s", writeErr, archive.Code, wantArchive, archive.Body.String())
				}
				after := cascadeCommercialFacts(t, pool)
				if archiveFirst {
					for _, table := range []string{"invoices", "estimates", "company_settings", "issued_document_numbers"} {
						historyEqual(t, "losing writer leaves "+table+" unchanged", after[table], before[table])
					}
				}
				var closed bool
				historyCheck(t, pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM customers WHERE id=$1`, c.ID).Scan(&closed))
				if closed != (wantArchive == 303) {
					t.Fatal("Customer outcome inconsistent")
				}
			})
		}
	}
}
