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
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/delivery"
)

func TestDeliveryRetryDependenciesHTTP(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retry dependencies").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Original").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Original").SaveX(ctx)
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
	historyCheck(t, err)
	expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
	fieldPost(t, web, cookie, path+"/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"retry@example.test"}, "subject": {"Frozen"}, "body": {"Body"}}, 303)
	_, err = delivery.New(pool, "").ProcessOne(ctx, &compositionSender{}, nil)
	historyCheck(t, err)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM document_deliveries`).Scan(&id))
	token, err := sessions.CreateMobile(ctx, admin, "Retry dependencies")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	retry := func(in delivery.RetryIntent, want int) {
		t.Helper()
		raw, err := json.Marshal(in)
		historyCheck(t, err)
		req := httptest.NewRequest("POST", "/delivery-retries", strings.NewReader(string(raw)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("retry: %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	accepted := delivery.RetryIntent{OperationID: newOperationKey(), DocumentType: "estimate", DocumentID: doc.ID, JobID: job.ID, DeliveryID: id, Reason: "Keep accepted communication"}
	retry(accepted, 200)
	failed := accepted
	failed.OperationID = newOperationKey()
	failed.JobID++
	retry(failed, 409)
	newOp := func(deps ...string) map[string]any {
		var revision string
		historyCheck(t, pool.QueryRow(ctx, `SELECT notes_revision::text FROM jobs WHERE id=$1`, job.ID).Scan(&revision))
		if deps == nil {
			deps = []string{}
		}
		return map[string]any{"operation_id": newOperationKey(), "type": "job.field_notes", "target": fmt.Sprint(job.ID), "depends_on": deps, "notes_revision": revision, "field_notes": ""}
	}
	submit := func(t *testing.T, wants []string, ops ...map[string]any) {
		t.Helper()
		raw, err := json.Marshal(map[string]any{"operations": ops})
		historyCheck(t, err)
		w := batchRequest(api, token, string(raw))
		if w.Code != 200 {
			t.Fatalf("batch: %d %s", w.Code, w.Body.String())
		}
		var out struct {
			Data struct {
				Results []struct {
					Receipt *authorization.JobExecutionReceipt `json:"receipt"`
				} `json:"results"`
			} `json:"data"`
		}
		historyCheck(t, json.Unmarshal(w.Body.Bytes(), &out))
		if len(out.Data.Results) != len(wants) {
			t.Fatal(w.Body.String())
		}
		for i, want := range wants {
			got := out.Data.Results[i].Receipt
			if got == nil || got.Code != want {
				t.Errorf("result %d want %s: %s", i, want, w.Body.String())
				continue
			}
			var durable string
			historyCheck(t, pool.QueryRow(ctx, `SELECT code FROM job_execution_receipts WHERE operation_id=$1`, ops[i]["operation_id"]).Scan(&durable))
			if durable != want {
				t.Errorf("durable outcome %s want %s", durable, want)
			}
		}
	}
	t.Run("accepted_and_failed_prerequisites_with_unrelated_continuation", func(t *testing.T) {
		unrelated := newOp()
		unrelated["field_notes"] = "unrelated continued"
		submit(t, []string{"accepted", "failed_dependency", "accepted"}, newOp(accepted.OperationID), newOp(failed.OperationID), unrelated)
		var notes string
		historyCheck(t, pool.QueryRow(ctx, `SELECT tech_notes FROM jobs WHERE id=$1`, job.ID).Scan(&notes))
		if notes != "unrelated continued" {
			t.Fatal("failed prerequisite blocked unrelated effect", notes)
		}
	})
	t.Run("explicit_new_ID_descendant_repair", func(t *testing.T) {
		child := newOp(failed.OperationID)
		child["field_notes"] = "explicitly repaired notes"
		submit(t, []string{"failed_dependency"}, child)
		repaired := accepted
		repaired.OperationID = newOperationKey()
		retry(repaired, 200)
		submit(t, []string{"failed_dependency"}, child) // A new prerequisite cannot revive this receipt.
		oldID := child["operation_id"]
		child["operation_id"] = newOperationKey()
		child["depends_on"] = []string{repaired.OperationID}
		submit(t, []string{"accepted"}, child) // Original target, revision and content stay exact.
		var notes, outcome string
		historyCheck(t, pool.QueryRow(ctx, `SELECT tech_notes FROM jobs WHERE id=$1`, job.ID).Scan(&notes))
		historyCheck(t, pool.QueryRow(ctx, `SELECT code FROM job_execution_receipts WHERE operation_id=$1`, oldID).Scan(&outcome))
		if notes != "explicitly repaired notes" || outcome != "failed_dependency" {
			t.Fatal(notes, outcome)
		}
	})
	t.Run("foreign_client_and_unknown_are_indistinguishable", func(t *testing.T) {
		original := token
		defer func() { token = original }()
		token, err = sessions.CreateMobile(ctx, admin, "Other retry client")
		historyCheck(t, err)
		submit(t, []string{"invalid_dependency", "invalid_dependency", "invalid_dependency"}, newOp(accepted.OperationID), newOp(failed.OperationID), newOp(newOperationKey()))
	})
	t.Run("foreign_deployment_and_unknown_are_indistinguishable", func(t *testing.T) {
		// Use an independently migrated deployment, retaining both databases'
		// immutable identity and foreign-key constraints.
		var original string
		historyCheck(t, pool.QueryRow(ctx, `SELECT id::text FROM operation_deployment`).Scan(&original))
		otherPool, otherClient, _, otherSessions, otherCompany, otherAdmin := permissionHTTPFixture(t)
		var other string
		historyCheck(t, otherPool.QueryRow(ctx, `SELECT id::text FROM operation_deployment`).Scan(&other))
		if original == other {
			t.Fatal("fixture reused deployment identity")
		}
		otherCustomer := otherClient.Customer.Create().SetCompanyID(otherCompany).SetDisplayName("Other deployment").SaveX(ctx)
		otherJob := otherClient.Job.Create().SetCompanyID(otherCompany).SetCustomerID(otherCustomer.ID).SetJobType("Other").SaveX(ctx)
		pool, job = otherPool, otherJob
		api = apiv1.NewRouter(otherPool, otherClient, otherSessions)
		token, err = otherSessions.CreateMobile(ctx, otherAdmin, "Other deployment")
		historyCheck(t, err)
		submit(t, []string{"invalid_dependency", "invalid_dependency", "invalid_dependency"}, newOp(accepted.OperationID), newOp(failed.OperationID), newOp(newOperationKey()))
	})
}
