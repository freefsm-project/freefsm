package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"

	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestDeliveryAcceptanceCommercialABAHTTP(t *testing.T) {
	for _, change := range []string{"unchanged", "edited", "ABA"} {
		t.Run(change, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Acceptance customer").SaveX(ctx)
			values := url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Original proposal"}, "line_items": {"[]"}, "tax_rate": {"0"}}
			documentTransactionRequest(t, web, cookie, "POST", "/estimates", values, 303)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id))
			path := fmt.Sprintf("/estimates/%d", id)
			_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='owned@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
			historyCheck(t, err)
			expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
			version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
			fieldPost(t, web, cookie, path+"/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"owned@example.test"}, "subject": {"Reviewed proposal"}, "body": {"Original"}}, 303)
			if change != "unchanged" {
				values.Set("title", "Intervening edit")
				documentTransactionRequest(t, web, cookie, "POST", path, currentCommercialFormValues(t, web, cookie, path, values), 303)
				if change == "ABA" {
					values.Set("title", "Original proposal")
					documentTransactionRequest(t, web, cookie, "POST", path, currentCommercialFormValues(t, web, cookie, path, values), 303)
				}
			}
			capture := &compositionSender{}
			worked, err := delivery.New(pool, "").ProcessOne(ctx, capture, statusflow.NewAcceptanceHook(statusflow.New(pool)))
			historyCheck(t, err)
			if !worked || len(capture.messages) != 1 {
				t.Fatal("missing immutable send")
			}
			var category, state string
			historyCheck(t, pool.QueryRow(ctx, `SELECT s.category_key,d.state FROM estimates e JOIN statuses s ON s.id=e.status_id JOIN document_deliveries d ON d.document_id=e.id AND d.document_type='estimate' WHERE e.id=$1`, id).Scan(&category, &state))
			want := "estimate:draft"
			if change == "unchanged" {
				want = "estimate:sent"
			}
			if state != "accepted" || category != want {
				t.Fatalf("acceptance state=%s category=%s want=%s", state, category, want)
			}
			assertContains(t, requestBody(t, web, cookie, "GET", path, 200), "accepted")
		})
	}
}
