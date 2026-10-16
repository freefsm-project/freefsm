package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestCommercialLifecycleStatuslessLegacyReadAndExplicitRepairAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained statusless Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Retained Job").SaveX(ctx)
	// The migrated schema intentionally retains nullable historical statuses.
	// Do not use new-document services, which correctly assign Draft defaults.
	estimate := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Retained Estimate").SaveX(ctx)
	invoice := client.Invoice.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Retained Invoice").SetInvoiceDate(time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC)).SetDueDate(time.Time{}).SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Legacy lifecycle review")
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []struct {
		kind string
		id   int64
	}{{"estimate", estimate.ID}, {"invoice", invoice.ID}} {
		t.Run(record.kind, func(t *testing.T) {
			before := documentTransactionSnapshot(t, ctx, pool)
			path := fmt.Sprintf("/%ss/%d", record.kind, record.id)
			body := lifecycleCall(t, api, token, "GET", path, "", 200)
			var response struct {
				Data map[string]json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal([]byte(body), &response); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"status_id", "manual_category", "effective_category"} {
				if string(response.Data[key]) != "null" {
					t.Fatalf("retained %s=%s, want explicit null", key, response.Data[key])
				}
			}
			var revision string
			if err := json.Unmarshal(response.Data["revision"], &revision); err != nil || revision == "" {
				t.Fatalf("missing reviewed revision: %s", body)
			}
			var workflow []struct {
				ID       int64  `json:"id"`
				Category string `json:"category"`
				Meaning  string `json:"meaning_revision"`
			}
			if err := json.Unmarshal(response.Data["workflow"], &workflow); err != nil || len(workflow) == 0 {
				t.Fatalf("missing repair workflow: %s", body)
			}
			if record.kind == "invoice" && string(response.Data["settlement_locked"]) != "false" {
				t.Fatalf("statusless Invoice lost lock presentation: %s", body)
			}
			if after := documentTransactionSnapshot(t, ctx, pool); after != before {
				t.Fatal("reading retained state invented document, financial or workflow facts")
			}
			if record.kind != "estimate" {
				return
			}
			var target int64
			var meaning string
			for _, choice := range workflow {
				if choice.Category == "estimate:accepted" {
					target, meaning = choice.ID, choice.Meaning
				}
			}
			if target == 0 {
				t.Fatal("missing explicit acceptance choice")
			}
			intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"status_id":%d,"meaning_revision":%q}`, newOperationKey(), job.ID, revision, target, meaning)
			accepted := lifecycleCall(t, api, token, "POST", path+"/status", intent, 200)
			if replay := lifecycleCall(t, api, token, "POST", path+"/status", intent, 200); replay != accepted {
				t.Fatal("explicit repair replay changed receipt")
			}
			assertContains(t, lifecycleCall(t, api, token, "GET", path, "", 200), `"manual_category":"estimate:accepted"`)
			var transitions, payments int
			var originalUnknown bool
			if err := pool.QueryRow(ctx, `SELECT count(*),bool_and(metadata->'from_status_id'='null'::jsonb) FROM activity_logs WHERE object_type='estimate' AND object_id=$1 AND action='status_transitioned'`, record.id).Scan(&transitions, &originalUnknown); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM invoice_payments`).Scan(&payments); err != nil {
				t.Fatal(err)
			}
			if transitions != 1 || !originalUnknown || payments != 0 {
				t.Fatalf("repair fabricated prior history or money: transitions=%d originalUnknown=%v payments=%d", transitions, originalUnknown, payments)
			}
		})
	}
}
