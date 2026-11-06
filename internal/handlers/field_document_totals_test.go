package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These are the worked COM-03 golden vectors in commercial/calculation_test.go,
// exercised at production creation rather than recalculating expected results.
func TestFieldDocumentCreationGoldenTotalsHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Golden totals").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Golden totals").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Golden totals")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	validate := serviceEntrySchemaValidator(t, api)
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			for _, tc := range []struct {
				name, lines, adjustments, tax string
				pretax, taxCents, total       int64
				invalid, exempt               bool
			}{
				{"empty", `[]`, `{}`, "0", 0, 0, 0, false, false},
				{"half negative", `[{"title":"positive","quantity":"1","unit_price":"1.00","taxable":false},{"title":"signed fractional","quantity":"0.5","unit_price":"-0.01","taxable":false}]`, `{}`, "0", 99, 0, 99, false, false},
				{"exempt", `[{"title":"exempt","quantity":"1","unit_price":"1.00","taxable":true}]`, `{}`, "100", 100, 0, 100, false, true},
				{"discount exhaustion", `[{"title":"discounted","quantity":"1","unit_price":"1.00","taxable":true}]`, `{"discount":{"kind":"fixed","value":"1000.00"},"surcharge":{"kind":"percentage","value":"10"}}`, "10", 0, 0, 0, false, false},
				{"unallocatable surcharge", `[]`, `{"surcharge":{"kind":"fixed","value":"0.01"}}`, "0", 0, 0, 0, true, false},
				{"zero surcharge", `[]`, `{"surcharge":{"kind":"fixed","value":"0.00"}}`, "0", 0, 0, 0, false, false},
				{"negative pretax", `[{"title":"negative","quantity":"1","unit_price":"-0.01","taxable":false}]`, `{}`, "0", 0, 0, 0, true, false},
				{"negative final", `[{"title":"positive","quantity":"1","unit_price":"0.02","taxable":false},{"title":"negative tax","quantity":"1","unit_price":"-0.02","taxable":true}]`, `{}`, "100", 0, 0, 0, true, false},
				{"negative adjustment even rounded zero", `[{"title":"negative","quantity":"0.01","unit_price":"-0.01","taxable":false,"discount_adjustment":{"kind":"fixed","value":"0.00"}}]`, `{}`, "0", 0, 0, 0, true, false},
				{"nonfinite quantity", `[{"title":"nonfinite","quantity":"NaN","unit_price":"1.00","taxable":false}]`, `{}`, "0", 0, 0, 0, true, false},
				{"nonfinite price", `[{"title":"nonfinite","quantity":"1","unit_price":"Infinity","taxable":false}]`, `{}`, "0", 0, 0, 0, true, false},
				{"zero quantity", `[{"title":"zero","quantity":"0","unit_price":"1.00","taxable":false}]`, `{}`, "0", 0, 0, 0, true, false},
				{"negative quantity", `[{"title":"negative","quantity":"-1","unit_price":"1.00","taxable":false}]`, `{}`, "0", 0, 0, 0, true, false},
				{"staged rounding", `[{"title":"fractional","quantity":"0.335","unit_price":"3.00","taxable":true,"discount_adjustment":{"kind":"percentage","value":"50"},"surcharge_adjustment":{"kind":"percentage","value":"10"}},{"title":"tax rounding","quantity":"1","unit_price":"0.05","taxable":true}]`, `{}`, "10", 61, 7, 68, false, false},
				{"stable allocation ties", `[{"title":"first","quantity":"1","unit_price":"0.01","taxable":true},{"title":"second","quantity":"1","unit_price":"0.01","taxable":true},{"title":"third","quantity":"1","unit_price":"0.01","taxable":false}]`, `{"discount":{"kind":"fixed","value":"0.01"},"surcharge":{"kind":"fixed","value":"0.01"}}`, "50", 3, 1, 4, false, false},
				{"independent fixed percent", `[{"title":"both","quantity":"1","unit_price":"10.00","taxable":true,"discount_adjustment":{"kind":"fixed","value":"2.00"},"surcharge_adjustment":{"kind":"percentage","value":"25"}}]`, `{"discount":{"kind":"percentage","value":"50"},"surcharge":{"kind":"fixed","value":"1.00"}}`, "10", 600, 60, 660, false, false},
				{"independent percent fixed", `[{"title":"both","quantity":"1","unit_price":"10.00","taxable":true,"discount_adjustment":{"kind":"percentage","value":"50"},"surcharge_adjustment":{"kind":"fixed","value":"1.00"}}]`, `{"discount":{"kind":"fixed","value":"1.00"},"surcharge":{"kind":"percentage","value":"20"}}`, "10", 600, 60, 660, false, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					client.Customer.UpdateOneID(c.ID).SetTaxExempt(tc.exempt).SaveX(ctx)
					before := fieldCreationEffects(t, pool, company)
					body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Golden vector","tax_rate":%q,"line_items":%s,"adjustments":%s}`, newOperationKey(), newOperationKey(), j.ID, c.ID, tc.tax, tc.lines, tc.adjustments)
					w := serviceAPIRequest(api, token, "POST", "/"+kind+"s", json.RawMessage(body))
					if tc.invalid {
						if w.Code != 422 && w.Code != 400 {
							t.Fatalf("invalid accepted/status %d: %s", w.Code, w.Body.String())
						}
						validate("ErrorEnvelope", w.Body.Bytes())
						if after := fieldCreationEffects(t, pool, company); after != before {
							t.Fatalf("partial invalid creation: %s -> %s", before, after)
						}
						return
					}
					if w.Code != 200 {
						t.Fatalf("creation %d: %s", w.Code, w.Body.String())
					}
					validate("CommercialReceiptEnvelope", w.Body.Bytes())
					var receipt struct {
						Data struct {
							ID int64 `json:"id"`
						} `json:"data"`
					}
					historyCheck(t, json.Unmarshal(w.Body.Bytes(), &receipt))
					read := serviceAPIRequest(api, token, "GET", fmt.Sprintf("/%ss/%d", kind, receipt.Data.ID), nil)
					if read.Code != 200 {
						t.Fatalf("read %d: %s", read.Code, read.Body.String())
					}
					validate("CommercialReadEnvelope", read.Body.Bytes())
					var state struct {
						Data struct {
							Financial struct {
								Result struct {
									Pretax int64 `json:"pretax_cents,string"`
									Tax    int64 `json:"tax_cents,string"`
									Total  int64 `json:"total_cents,string"`
									Lines  []struct {
										Base     int64 `json:"base_cents,string"`
										Adjusted int64 `json:"adjusted_cents,string"`
									} `json:"lines"`
								} `json:"result"`
							} `json:"financial"`
						} `json:"data"`
					}
					historyCheck(t, json.Unmarshal(read.Body.Bytes(), &state))
					got := state.Data.Financial.Result
					if got.Pretax != tc.pretax || got.Tax != tc.taxCents || got.Total != tc.total {
						t.Fatalf("got %+v want %d/%d/%d", got, tc.pretax, tc.taxCents, tc.total)
					}
					if tc.name == "stable allocation ties" && (len(got.Lines) != 3 || got.Lines[0].Adjusted != 0 || got.Lines[1].Adjusted != 2 || got.Lines[2].Adjusted != 1) {
						t.Fatalf("wrong proportional allocation: %+v", got.Lines)
					}
					if tc.name == "staged rounding" && (len(got.Lines) != 2 || got.Lines[0].Base != 101 || got.Lines[0].Adjusted != 56) {
						t.Fatalf("wrong staged rounding: %+v", got.Lines)
					}
				})
			}
		})
	}
}

func fieldCreationEffects(t *testing.T, pool *pgxpool.Pool, company int64) string {
	t.Helper()
	var state string
	historyCheck(t, pool.QueryRow(context.Background(), `SELECT jsonb_build_array((SELECT count(*) FROM estimates),(SELECT count(*) FROM invoices),(SELECT count(*) FROM commercial_client_identities),(SELECT count(*) FROM authorization_audit WHERE action IN ('estimate.create','invoice.create','pricebook.view')),(SELECT count(*) FROM issued_document_numbers),(SELECT next_estimate_number FROM company_settings WHERE company_id=$1),(SELECT next_invoice_number FROM company_settings WHERE company_id=$1))::text`, company).Scan(&state))
	return state
}
