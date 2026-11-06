package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommercialExactPrecisionPersistsThroughAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Exact precision Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Exact precision Job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Exact precision")
	if err != nil {
		t.Fatal(err)
	}
	call := func(t *testing.T, method, path, body string) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	huge := "1" + strings.Repeat("0", 400)
	cases := []struct {
		name, quantityJSON, quantity, price, rate, total string
		taxable                                          bool
	}{
		{"unquantized tax rate", `"1"`, "1", "200.00", "0.005", "200.01", true},
		{"cents beyond IEEE754", `"1"`, "1", "90071992547409.93", "0", "90071992547409.93", false},
		{"compatible numeric exponent", `1e-7`, "0.0000001", "100000.00", "0", "0.01", false},
		{"finite exact quantity beyond float range", fmt.Sprintf("%q", huge), huge, "0.00", "0", "0.00", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Exact precision","tax_rate":%q,"line_items":[{"title":"Exact capture","quantity":%s,"unit_price":%q,"taxable":%t}]}`, newOperationKey(), newOperationKey(), j.ID, c.ID, tc.rate, tc.quantityJSON, tc.price, tc.taxable)
			var receipt struct {
				Data struct {
					ID int64 `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(call(t, "POST", "/invoices", body), &receipt); err != nil {
				t.Fatal(err)
			}
			var read struct {
				Data struct {
					Rate   string            `json:"tax_rate"`
					Totals map[string]string `json:"totals"`
					Lines  []struct {
						Quantity string `json:"quantity"`
						Price    string `json:"unit_price"`
					} `json:"line_items"`
				} `json:"data"`
			}
			if err := json.Unmarshal(call(t, "GET", fmt.Sprintf("/invoices/%d", receipt.Data.ID), ""), &read); err != nil {
				t.Fatal(err)
			}
			if read.Data.Rate != tc.rate || read.Data.Totals["total"] != tc.total || len(read.Data.Lines) != 1 || read.Data.Lines[0].Quantity != tc.quantity || read.Data.Lines[0].Price != tc.price {
				t.Fatalf("precision changed: %+v", read.Data)
			}
		})
	}
}
