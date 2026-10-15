package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestCommercialExactAdjustmentsPersistedAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Exact amounts customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Exact amounts Job").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Exact arithmetic tablet")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
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
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Exact staged amounts","tax_rate":"10","line_items":[{"title":"Fractional work","quantity":"0.335","unit_price":"3.00","taxable":true,"discount_adjustment":{"kind":"percentage","value":"50"},"surcharge_adjustment":{"kind":"percentage","value":"10"}},{"title":"Tax rounding","quantity":"1","unit_price":"0.05","taxable":true}]}`, newOperationKey(), newOperationKey(), j.ID)
			first := call(t, "POST", "/"+kind+"s", body)
			if replay := call(t, "POST", "/"+kind+"s", body); string(first) != string(replay) {
				t.Fatal("same intent changed receipt")
			}
			var receipt struct {
				Data struct {
					ID int64 `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(first, &receipt); err != nil {
				t.Fatal(err)
			}
			var read struct {
				Data struct {
					Financial struct {
						Version int `json:"version"`
						Result  struct {
							Pretax int64 `json:"pretax_cents,string"`
							Tax    int64 `json:"tax_cents,string"`
							Total  int64 `json:"total_cents,string"`
						} `json:"result"`
					} `json:"financial"`
				} `json:"data"`
			}
			if err := json.Unmarshal(call(t, "GET", fmt.Sprintf("/%ss/%d", kind, receipt.Data.ID), ""), &read); err != nil {
				t.Fatal(err)
			}
			f := read.Data.Financial
			if f.Version != 1 || f.Result.Pretax != 61 || f.Result.Tax != 7 || f.Result.Total != 68 {
				t.Fatalf("persisted accepted arithmetic = %+v", f)
			}
		})
	}
}
