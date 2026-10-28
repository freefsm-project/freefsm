package handlers

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestCustomerCascadeNewDependencyCommitOrdersHTTP(t *testing.T) {
	for _, kind := range []string{"job", "invoice", "credit", "contact", "location", "primary", "placement", "asset-transfer"} {
		for _, archiveFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/archiveFirst=%v", kind, archiveFirst), func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				cookie := sessionCookie(t, ctx, sessions, admin)
				c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Ordered cascade").SaveX(ctx)
				base := fmt.Sprintf("/customers/%d", c.ID)
				child := masterPost(t, web, cookie, base+"/contacts", url.Values{"first_name": {"Preserved child"}}, 303)
				var mutation creditCommand
				switch kind {
				case "job":
					mutation = creditCommand{"/jobs", url.Values{"form_mode": {"create"}, "job_id": {"0"}, "job_type": {"Racing work"}, "customer_id": {fmt.Sprint(c.ID)}}}
				case "invoice":
					mutation = creditCommand{"/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Racing zero Draft"}, "line_items": {"[]"}, "tax_rate": {"0"}}}
				case "credit":
					mutation = creditCommand{base + "/payments", url.Values{"idempotency_key": {newOperationKey()}, "amount": {"1.00"}, "method": {"cash"}, "date": {"2026-01-02"}}}
				case "contact":
					mutation = creditCommand{base + "/contacts", url.Values{"first_name": {"New child"}}}
				case "location":
					mutation = creditCommand{base + "/locations", url.Values{"title": {"New place"}}}
				case "primary":
					mutation = creditCommand{child, url.Values{"first_name": {"Preserved child"}, "is_primary": {"true"}}}
				case "placement":
					typ := client.AssetType.Create().SetCompanyID(company).SetName("Race equipment").SaveX(ctx)
					a := client.Asset.Create().SetCompanyID(company).SetOwnershipClass("company_owned").SetAssetTypeID(typ.ID).SetName("Placement candidate").SaveX(ctx)
					mutation = creditCommand{fmt.Sprintf("/assets/%d/associations/place", a.ID), url.Values{"customer_id": {fmt.Sprint(c.ID)}, "effective_date": {time.Now().UTC().Format(time.DateOnly)}, "reason": {"Explicit racing placement"}, "revision": {"0"}}}
				case "asset-transfer":
					typ := client.AssetType.Create().SetCompanyID(company).SetName("Transfer equipment").SaveX(ctx)
					other := client.Customer.Create().SetCompanyID(company).SetDisplayName("Transfer destination").SaveX(ctx)
					a := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(typ.ID).SetName("Included transfer candidate").SaveX(ctx)
					mutation = creditCommand{fmt.Sprintf("/assets/%d/associations/transfer", a.ID), url.Values{"customer_id": {fmt.Sprint(other.ID)}, "effective_date": {time.Now().UTC().Format(time.DateOnly)}, "reason": {"Explicit racing transfer"}, "revision": {"0"}}}
				}
				var preview authorization.CustomerArchivePreview
				masterJSON(t, web, cookie, base+"/archive", &preview)
				archive := creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}
				f := &creditMatrix{pool: pool, web: web, cookie: cookie, ctx: ctx}
				var archived, written *httptest.ResponseRecorder
				if archiveFirst {
					archived, written = f.ordered(t, archive, mutation)
				} else {
					written, archived = f.ordered(t, mutation, archive)
				}
				if archiveFirst {
					if archived.Code != 303 || written.Code < 400 {
						t.Fatalf("archive first: archive=%d mutation=%d %s", archived.Code, written.Code, written.Body.String())
					}
				} else if written.Code != 303 || archived.Code != 409 {
					t.Fatalf("mutation first: mutation=%d archive=%d %s", written.Code, archived.Code, written.Body.String())
				}
				var customerClosed, childClosed bool
				var audits int
				historyCheck(t, pool.QueryRow(ctx, `SELECT c.deleted_at IS NOT NULL,(SELECT bool_and(deleted_at IS NOT NULL) FROM customer_contacts WHERE customer_id=c.id),(SELECT count(*) FROM authorization_audit WHERE action='customer.archive' AND target_id=c.id) FROM customers c WHERE id=$1`, c.ID).Scan(&customerClosed, &childClosed, &audits))
				if customerClosed != archiveFirst || childClosed != archiveFirst || (audits == 1) != archiveFirst {
					t.Fatalf("partial cascade state: customer=%v child=%v audit=%d", customerClosed, childClosed, audits)
				}
			})
		}
	}
}

func TestCustomerCascadeFinancialReversalCommitOrdersHTTP(t *testing.T) {
	for _, kind := range []string{"application", "payment", "refund"} {
		for _, archiveFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/archiveFirst=%v", kind, archiveFirst), func(t *testing.T) {
				f := newCreditMatrix(t)
				base := fmt.Sprintf("/customers/%d", f.customer)
				invoice := fmt.Sprintf("/invoices/%d", f.invoice)
				var reversal creditCommand
				if kind == "application" {
					reversal = f.reverse(t, "100.00")
				} else {
					f.post(t, creditCommand{invoice + "/payments-with-credit", url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}}, 303)
					f.post(t, creditCommand{base + "/credit/refunds", url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "effective_date": {"2026-01-02"}, "reason": {"External return"}}}, 303)
					var id string
					if kind == "payment" {
						historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM invoice_payments WHERE invoice_id=$1`, f.invoice).Scan(&id))
						reversal = creditCommand{invoice + "/payments/" + id + "/reverse", url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"external_return"}}}
					} else {
						historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT id FROM credit_refunds`).Scan(&id))
						reversal = creditCommand{base + "/credit/refunds/" + id + "/reverse", url.Values{"idempotency_key": {newOperationKey()}, "effective_date": {"2026-01-02"}, "reason": {"Correct external refund"}}}
					}
				}
				var preview authorization.CustomerArchivePreview
				masterJSON(t, f.web, f.cookie, base+"/archive", &preview)
				if preview.Snapshot == "" {
					t.Fatalf("fixture not archive-eligible: %+v", preview)
				}
				archive := creditCommand{base + "/delete", url.Values{"archive_snapshot": {preview.Snapshot}}}
				tables := []string{"invoices", "invoice_payments", "customer_credits"}
				if kind == "application" {
					tables = append(tables, "credit_applications")
				} else {
					tables = append(tables, "credit_refunds", "credit_refund_allocations")
				}
				before := historyDurableRows(t, f.pool, tables)
				var archived, reversed *httptest.ResponseRecorder
				if archiveFirst {
					archived, reversed = f.ordered(t, archive, reversal)
				} else {
					reversed, archived = f.ordered(t, reversal, archive)
				}
				if archiveFirst {
					if archived.Code != 303 || reversed.Code < 400 {
						t.Fatalf("archive=%d reversal=%d", archived.Code, reversed.Code)
					}
					historyEqual(t, "archived Customer cannot regain debt or Credit", historyDurableRows(t, f.pool, tables), before)
				} else if reversed.Code != 303 || archived.Code != 409 {
					t.Fatalf("reversal=%d archive=%d: %s", reversed.Code, archived.Code, reversed.Body.String())
				}
				var reversals int
				historyCheck(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM settlement_reversals`).Scan(&reversals))
				if (archiveFirst && reversals != 0) || (!archiveFirst && reversals != 1) {
					t.Fatalf("unexpected reversal effects: %d", reversals)
				}
			})
		}
	}
}
