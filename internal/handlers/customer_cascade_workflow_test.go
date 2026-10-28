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

func TestCustomerCascadeVoidHistoryCannotReopenDebtHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Void retained history").SaveX(ctx)
	base := fmt.Sprintf("/customers/%d", c.ID)
	fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Retained Void debt"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}}, 303)
	var id, void, invoiced int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void'`, company).Scan(&void))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced'`, company).Scan(&invoiced))
	workflow := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	historyCheck(t, workflow.FinalizeInvoice(ctx, actor, id))
	historyCheck(t, workflow.TransitionInvoice(ctx, actor, id, void))
	path := fmt.Sprintf("/invoices/%d", id)
	values := commercialReviewedValues(t, web, cookie, path, url.Values{"status_id": {fmt.Sprint(invoiced)}})
	var preview authorization.CustomerArchivePreview
	masterJSON(t, web, cookie, base+"/archive", &preview)
	masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {preview.Snapshot}}, 303)
	before := historyDurableRows(t, pool, []string{"customers", "invoices", "authorization_audit", "activity_logs"})
	documentTransactionRequest(t, web, cookie, "POST", path+"/status", values, 409)
	historyEqual(t, "retained Void cannot reopen debt through reviewed HTTP workflow", historyDurableRows(t, pool, []string{"customers", "invoices", "authorization_audit", "activity_logs"}), before)
	if err := workflow.TransitionInvoice(ctx, actor, id, invoiced); err == nil {
		t.Fatal("legacy workflow reopened debt under archived Customer")
	}
	historyEqual(t, "legacy workflow cannot recreate debt", historyDurableRows(t, pool, []string{"customers", "invoices", "authorization_audit", "activity_logs"}), before)
	masterPost(t, web, cookie, base+"/restore", nil, 303)
	historyCheck(t, workflow.TransitionInvoice(ctx, actor, id, invoiced))
}

func TestCustomerCascadeWorkflowCommitOrdersHTTP(t *testing.T) {
	for _, archiveFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(archiveFirst), func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Void workflow race").SaveX(ctx)
			base := fmt.Sprintf("/customers/%d", c.ID)
			fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Void debt"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}}, 303)
			var id, void, invoiced int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id))
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void'`, company).Scan(&void))
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced'`, company).Scan(&invoiced))
			workflow := statusflow.New(pool)
			actor := statusflow.Actor{ID: admin, CompanyID: company}
			historyCheck(t, workflow.FinalizeInvoice(ctx, actor, id))
			historyCheck(t, workflow.TransitionInvoice(ctx, actor, id, void))
			path := fmt.Sprintf("/invoices/%d", id)
			values := commercialReviewedValues(t, web, cookie, path, url.Values{"status_id": {fmt.Sprint(invoiced)}})
			var preview authorization.CustomerArchivePreview
			masterJSON(t, web, cookie, base+"/archive", &preview)
			archive := creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}
			transition := creditCommand{path + "/status", values}
			f := &creditMatrix{pool: pool, web: web, cookie: cookie, ctx: ctx}
			var archived, changed *httptest.ResponseRecorder
			if archiveFirst {
				archived, changed = f.ordered(t, archive, transition)
			} else {
				changed, archived = f.ordered(t, transition, archive)
			}
			if archiveFirst {
				if archived.Code != 303 || changed.Code != 409 {
					t.Fatalf("archive=%d workflow=%d", archived.Code, changed.Code)
				}
			} else if changed.Code != 303 || archived.Code != 409 {
				t.Fatalf("workflow=%d archive=%d", changed.Code, archived.Code)
			}
			var archivedCustomer bool
			var status int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT c.deleted_at IS NOT NULL,i.status_id FROM customers c JOIN invoices i ON i.customer_id=c.id WHERE i.id=$1`, id).Scan(&archivedCustomer, &status))
			if archivedCustomer != archiveFirst || (archiveFirst && status != void) || (!archiveFirst && status != invoiced) {
				t.Fatalf("invalid debt/archive result: archived=%v status=%d", archivedCustomer, status)
			}
		})
	}
}
