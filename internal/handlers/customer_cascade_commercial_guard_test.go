package handlers

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Empty document/ledger sets are intentional in creation-denial cases. Compare
// the complete sets as well as number allocation and immutable audit, not just
// a response or one lifecycle flag.
func cascadeCommercialFacts(t *testing.T, pool *pgxpool.Pool) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, table := range []string{"customers", "invoices", "estimates", "company_settings", "issued_document_numbers", "authorization_audit", "activity_logs"} {
		var rows string
		historyCheck(t, pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]')::text FROM `+table+` r`).Scan(&rows))
		out[table] = rows
	}
	return out
}

func TestCustomerCascadeCommercialDirectCreationRequiresRestore(t *testing.T) {
	for _, kind := range []string{"invoice-ent", "invoice-pool", "estimate"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Direct creation owner").SaveX(ctx)
			base := fmt.Sprintf("/customers/%d", c.ID)
			var review authorization.CustomerArchivePreview
			masterJSON(t, web, cookie, base+"/archive", &review)
			masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 303)
			write := func() error {
				if kind == "estimate" {
					_, err := services.NewEstimateService(client).Create(ctx, services.EstimateCreateParams{CustomerID: c.ID, Title: "New proposal", TaxRate: "0"})
					return err
				}
				s := services.NewInvoiceService(client)
				if kind == "invoice-pool" {
					s = services.NewInvoiceService(client, pool)
				}
				_, err := s.Create(ctx, services.InvoiceCreateParams{CustomerID: c.ID, Title: "New zero Draft", TaxRate: "0", InvoiceDate: time.Now(), DueDate: time.Now()})
				return err
			}
			before := cascadeCommercialFacts(t, pool)
			if err := write(); err == nil {
				t.Fatal("direct service created a document beneath archived Customer")
			}
			historyEqual(t, "creation rejection preserves documents, numbering and audit", cascadeCommercialFacts(t, pool), before)
			masterPost(t, web, cookie, base+"/restore", nil, 303)
			historyCheck(t, write())
		})
	}
}

func TestCustomerCascadeCommercialDirectAndHTTPDetailsRequireRestore(t *testing.T) {
	for _, mode := range []string{"invoice-money", "invoice-title", "invoice-http", "estimate-title", "estimate-http"} {
		t.Run(mode, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained commercial owner").SaveX(ctx)
			base := fmt.Sprintf("/customers/%d", c.ID)
			kind := "invoice"
			if mode == "estimate-title" || mode == "estimate-http" {
				kind = "estimate"
			}
			fieldPost(t, web, cookie, "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Retained original"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM `+kind+`s`).Scan(&id))
			path := fmt.Sprintf("/%ss/%d", kind, id)
			values := commercialReviewedValues(t, web, cookie, path, url.Values{"title": {"New content"}, "notes": {"Operational edit"}})
			if kind == "invoice" {
				historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id))
				values = commercialReviewedValues(t, web, cookie, path, url.Values{"title": {"New content"}, "notes": {"Operational edit"}})
				var review authorization.CustomerArchivePreview
				masterJSON(t, web, cookie, base+"/archive", &review)
				masterPost(t, web, cookie, base+"/delete", url.Values{"archive_snapshot": {review.Snapshot}}, 303)
			} else {
				// Installed legacy history can contain an unarchived proposal beneath
				// an archived owner. Preserve it; do not pretend the modern cascade
				// would permit this fixture's historical state.
				_, err := client.Customer.UpdateOneID(c.ID).SetDeletedAt(time.Now()).Save(ctx)
				historyCheck(t, err)
			}
			values.Set("customer_id", fmt.Sprint(c.ID))
			if kind == "invoice" {
				var number int64
				historyCheck(t, pool.QueryRow(ctx, `SELECT invoice_number FROM invoices WHERE id=$1`, id).Scan(&number))
				values.Set("invoice_number", fmt.Sprint(number))
			}
			before := cascadeCommercialFacts(t, pool)
			write := func() error {
				title := "New content"
				if kind == "estimate" {
					_, err := services.NewEstimateService(client).Update(ctx, id, services.EstimateUpdateParams{Title: &title})
					return err
				}
				params := services.InvoiceUpdateParams{Title: &title}
				if mode == "invoice-money" {
					lines := []services.LineItem{{Title: "New debt", Quantity: 1, UnitPrice: 100}}
					params = services.InvoiceUpdateParams{LineItems: &lines}
				}
				_, err := services.NewInvoiceService(client, pool).Update(ctx, id, params)
				return err
			}
			if mode == "invoice-http" || mode == "estimate-http" {
				documentTransactionRequest(t, web, cookie, "POST", path, values, 409)
			} else if err := write(); err == nil {
				t.Fatal("direct details writer accepted archived Customer")
			}
			historyEqual(t, "details rejection preserves commercial facts and audit", cascadeCommercialFacts(t, pool), before)
			requestBody(t, web, cookie, "GET", path, 200)
			masterPost(t, web, cookie, base+"/restore", nil, 303)
			if mode == "invoice-http" || mode == "estimate-http" {
				documentTransactionRequest(t, web, cookie, "POST", path, commercialReviewedValues(t, web, cookie, path, values), 303)
			} else {
				historyCheck(t, write())
			}
		})
	}
}
