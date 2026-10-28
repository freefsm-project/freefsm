package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestCustomerCascadeDocumentEligibilityHTTP(t *testing.T) {
	for _, kind := range []string{"zero-draft", "outstanding", "paid", "void", "estimate", "converted"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Document eligibility").SaveX(ctx)
			base := fmt.Sprintf("/customers/%d", c.ID)
			docKind := "invoice"
			if kind == "estimate" || kind == "converted" {
				docKind = "estimate"
			}
			lines := "[]"
			if kind == "outstanding" {
				lines = `[{"title":"Work","quantity":1,"unit_price":100}]`
			}
			fieldPost(t, web, cookie, "/"+docKind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Eligibility document"}, "line_items": {lines}, "tax_rate": {"0"}}, 303)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM `+docKind+`s WHERE customer_id=$1`, c.ID).Scan(&id))
			if kind == "converted" {
				path := fmt.Sprintf("/estimates/%d/convert", id)
				fieldPost(t, web, cookie, path, conversionReview(t, web, cookie, path), 303)
				historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1`, id).Scan(&id))
			}
			if kind == "paid" || kind == "outstanding" || kind == "converted" || kind == "void" {
				historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id))
			}
			if kind == "void" {
				var status int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void'`, company).Scan(&status))
				historyCheck(t, statusflow.New(pool).TransitionInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id, status))
			}
			var review authorization.CustomerArchivePreview
			masterJSON(t, web, cookie, base+"/archive", &review)
			blocked := kind == "zero-draft" || kind == "outstanding" || kind == "estimate"
			if (len(review.Blockers) > 0) != blocked || (review.Snapshot == "") != blocked {
				t.Fatalf("%s eligibility %+v", kind, review)
			}
			if blocked {
				masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {"forged"}}, 409)
				return
			}
			before := historyDurableRows(t, pool, []string{"invoices"})
			masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 303)
			requestBody(t, web, cookie, "GET", fmt.Sprintf("/invoices/%d", id), 200)
			masterPost(t, web, cookie, base+"/restore", nil, 303)
			historyEqual(t, "archive/restore never change Paid/Void or converted document history", historyDurableRows(t, pool, []string{"invoices"}), before)
		})
	}
}

func TestCustomerCascadeLeaseStatesAndCommitOrders(t *testing.T) {
	for _, state := range []string{"draft", "scheduled", "active", "month_to_month", "terminated"} {
		t.Run(state, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Lease owner").SaveX(ctx)
			base := fmt.Sprintf("/customers/%d", c.ID)
			tx, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer tx.Rollback(ctx)
			historyCheck(t, authorization.New(pool).WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, authorization.LeaseDependencies{LeaseID: 64, CustomerID: c.ID, Lifecycle: state}))
			historyCheck(t, tx.Commit(ctx))
			var review authorization.CustomerArchivePreview
			masterJSON(t, web, cookie, base+"/archive", &review)
			if state != "terminated" {
				if len(review.Blockers) != 1 || review.Blockers[0].Kind != "lease" || review.Blockers[0].ID != "64" || review.Snapshot != "" {
					t.Fatalf("lease missing from review %+v", review)
				}
				masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {"forged"}}, 409)
			} else {
				before := historyDurableRows(t, pool, []string{"lease_master_dependencies"})
				masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 303)
				masterPost(t, web, cookie, base+"/restore", nil, 303)
				historyEqual(t, "terminated Lease history unchanged", historyDurableRows(t, pool, []string{"lease_master_dependencies"}), before)
			}
		})
	}
	for _, leaseFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("race/leaseFirst=%v", leaseFirst), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Lease race").SaveX(ctx)
			cookie := sessionCookie(t, ctx, sessions, admin)
			base := fmt.Sprintf("/customers/%d", c.ID)
			var preview authorization.CustomerArchivePreview
			masterJSON(t, web, cookie, base+"/archive", &preview)
			barrier, err := pool.Begin(ctx)
			historyCheck(t, err)
			defer barrier.Rollback(context.Background())
			var pid int
			historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(190019)`).Scan(&pid))
			leaseDone := make(chan error, 1)
			archiveDone := make(chan *httptest.ResponseRecorder, 1)
			lease := func() {
				tx, e := pool.Begin(ctx)
				if e != nil {
					leaseDone <- e
					return
				}
				defer tx.Rollback(context.Background())
				e = authorization.New(pool).WriteLeaseDependencies(ctx, tx, authorization.Actor{UserID: admin}, authorization.LeaseDependencies{LeaseID: 64, CustomerID: c.ID, Lifecycle: "draft"})
				if e == nil {
					e = tx.Commit(ctx)
				}
				leaseDone <- e
			}
			archive := func() {
				archiveDone <- (&creditMatrix{pool: pool, web: web, cookie: cookie, ctx: ctx}).call(creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}})
			}
			if leaseFirst {
				go lease()
			} else {
				go archive()
			}
			commercialWaiters(t, ctx, pool, pid, 1)
			var first int
			historyCheck(t, pool.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND application_name=$2`, pid, pool.Config().ConnConfig.RuntimeParams["application_name"]).Scan(&first))
			if leaseFirst {
				go archive()
			} else {
				go lease()
			}
			commercialWaiters(t, ctx, pool, first, 1)
			t.Logf("observed Lease/Customer ordering holder=%d first=%d leaseFirst=%v", pid, first, leaseFirst)
			historyCheck(t, barrier.Commit(ctx))
			leaseErr, archived := <-leaseDone, <-archiveDone
			if leaseFirst {
				if leaseErr != nil || archived.Code != 409 {
					t.Fatalf("lease=%v archive=%d", leaseErr, archived.Code)
				}
			} else if leaseErr == nil || archived.Code != 303 {
				t.Fatalf("archive=%d lease=%v", archived.Code, leaseErr)
			}
		})
	}
}
