package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestJobPricingRepairExactUSDHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Exact USD customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Exact USD capture").SaveX(ctx)
	path := fmt.Sprintf("/jobs/%d", job.ID)
	lines := `[{"title":"Whole dollars","unit_price":5,"quantity":2},{"title":"Fractional cents","description":"Retained precision","unit_price":1.005,"quantity":1.2345,"discount":0.005,"surcharge":2.675,"taxable":true,"tax_rate":"8.125%"},{"title":"Below half cent","unit_price":0.004,"quantity":1,"discount":0.004,"surcharge":0.004}]`
	itemPost(t, web, sessionCookie(t, ctx, sessions, admin), path, url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(job.ID)}, "line_items": {lines}}, 303)
	before := client.Job.GetX(ctx, job.ID).LineItems
	if !strings.Contains(before, "1.005") || !strings.Contains(before, "2.675") {
		t.Fatal("capture must retain fractional-cent input")
	}
	token, err := sessions.CreateMobile(ctx, admin, "Exact USD reader")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	type capture struct {
		SnapshotID  string  `json:"snapshot_id"`
		UnitPrice   string  `json:"unit_price"`
		Discount    string  `json:"discount"`
		Surcharge   string  `json:"surcharge"`
		Quantity    float64 `json:"quantity"`
		TaxRate     string  `json:"tax_rate"`
		Taxable     bool    `json:"taxable"`
		Description string  `json:"description"`
	}
	type publicJob struct {
		Lines []capture `json:"line_items"`
	}
	for _, target := range []string{path, "/jobs"} {
		t.Run(target, func(t *testing.T) {
			r := httptest.NewRequest("GET", target, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			w := httptest.NewRecorder()
			api.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("GET: %d %s", w.Code, w.Body.String())
			}
			var envelope struct{ Data json.RawMessage }
			if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			var got publicJob
			if target == path {
				err = json.Unmarshal(envelope.Data, &got)
			} else {
				var jobs []publicJob
				err = json.Unmarshal(envelope.Data, &jobs)
				if err == nil && len(jobs) == 1 {
					got = jobs[0]
				}
			}
			if err != nil || len(got.Lines) != 3 {
				t.Fatalf("exact string pricing: %s (%v)", envelope.Data, err)
			}
			for n, want := range [][3]string{{"5.00", "0.00", "0.00"}, {"1.01", "0.01", "2.68"}, {"0.00", "0.00", "0.00"}} {
				line := got.Lines[n]
				if [3]string{line.UnitPrice, line.Discount, line.Surcharge} != want || line.SnapshotID == "" {
					t.Errorf("line %d: %+v, want USD %v and retained identity", n, line, want)
				}
			}
			if line := got.Lines[1]; line.Quantity != 1.2345 || line.TaxRate != "8.125%" || !line.Taxable || line.Description != "Retained precision" {
				t.Errorf("non-money semantics changed: %+v", line)
			}
		})
	}
	if after := client.Job.GetX(ctx, job.ID).LineItems; after != before {
		t.Fatal("public projection rewrote persisted snapshots")
	}
}
