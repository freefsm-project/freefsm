package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Contract failures remain test failures, but do not hide persisted-effect
// failures behind the first unsupported schema variant in an unfinished API.
func commercialBatchCall(t *testing.T, api http.Handler, token, body string) string {
	t.Helper()
	return commercialBatchHTTP(t, api, token, body, http.StatusOK)
}

func commercialBatchHTTP(t *testing.T, api http.Handler, token, body string, wantStatus int) string {
	t.Helper()
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	if document.Code != http.StatusOK {
		t.Fatalf("served OpenAPI status %d", document.Code)
	}
	var spec map[string]any
	historyCheck(t, json.Unmarshal(document.Body.Bytes(), &spec))
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	historyCheck(t, compiler.AddResource("https://commercial-batch.test/openapi.json", spec))
	validate := func(name, raw string) {
		t.Helper()
		schema, err := compiler.Compile("https://commercial-batch.test/openapi.json#/components/schemas/" + name)
		historyCheck(t, err)
		value, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
		historyCheck(t, err)
		err = schema.Validate(value)
		if name == "OperationBatch" && wantStatus == http.StatusBadRequest {
			if err == nil {
				t.Error("malformed request unexpectedly conforms to OperationBatch")
			}
			return
		}
		if err != nil {
			t.Errorf("%s contract: %v", name, err)
		} else {
			t.Logf("%s contract verified", name)
		}
	}
	validate("OperationBatch", body)
	r := httptest.NewRequest("POST", "/api/v1/sync/operations", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	http.StripPrefix("/api/v1", api).ServeHTTP(w, r)
	operation := spec["paths"].(map[string]any)["/sync/operations"].(map[string]any)["post"].(map[string]any)
	if _, ok := operation["responses"].(map[string]any)[fmt.Sprint(w.Code)]; !ok {
		t.Errorf("undocumented batch status %d", w.Code)
	}
	name := "OperationBatchEnvelope"
	if w.Code >= 400 {
		name = "ErrorEnvelope"
	}
	validate(name, w.Body.String())
	if w.Code != wantStatus {
		t.Fatalf("batch status %d: %s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestCommercialBatchAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Batch Customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Batch Job").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, job.ID, admin)
			token, err := sessions.CreateMobile(ctx, admin, "Commercial batch")
			historyCheck(t, err)
			api := apiv1.NewRouter(pool, client, sessions)
			created := lifecycleCall(t, api, token, "POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Batch document"}`, newOperationKey(), newOperationKey(), job.ID, customer.ID), 200)
			var receipt struct {
				Data struct {
					ID       int64  `json:"id"`
					Revision string `json:"revision"`
				} `json:"data"`
			}
			historyCheck(t, json.Unmarshal([]byte(created), &receipt))
			id := newOperationKey()
			body := fmt.Sprintf(`{"operations":[{"operation_id":%q,"type":%q,"target":%q,"depends_on":[],"commercial":{"job_id":%d,"customer_id":%d,"revision":%q,"notes":"Ordered edit"}}]}`, id, kind+".edit", fmt.Sprint(receipt.Data.ID), job.ID, customer.ID, receipt.Data.Revision)
			first := commercialBatchCall(t, api, token, body)
			var notes, outcome string
			historyCheck(t, pool.QueryRow(ctx, "SELECT coalesce(notes,'') FROM "+kind+"s WHERE id=$1", receipt.Data.ID).Scan(&notes))
			if notes != "Ordered edit" {
				t.Fatalf("persisted edit missing: %q", notes)
			}
			assertContains(t, first, `"outcome":"accepted"`)
			if replay := commercialBatchCall(t, api, token, body); replay != first {
				t.Fatalf("batch replay changed receipt: %s / %s", first, replay)
			}
			state := lifecycleCall(t, api, token, "GET", fmt.Sprintf("/%ss/%d", kind, receipt.Data.ID), "", 200)
			assertContains(t, state, `"notes":"Ordered edit"`)
			historyCheck(t, pool.QueryRow(ctx, `SELECT receipt->>'outcome' FROM commercial_operations WHERE operation_id=$1`, id).Scan(&outcome))
			if outcome != "accepted" {
				t.Fatalf("persisted receipt %q", outcome)
			}
			t.Log("persisted commercial edit and original replay receipt verified")
			// The original revision is deliberately retained, not rewritten from
			// the first result. A known-success dependency allows evaluation, but
			// cannot authorize a stale edit. Its descendant must remain blocked.
			stale, blocked := newOperationKey(), newOperationKey()
			dependencyBody := fmt.Sprintf(`{"operations":[{"operation_id":%q,"type":%q,"target":%q,"depends_on":[%q],"commercial":{"job_id":%d,"revision":%q,"notes":"Stale edit"}},{"operation_id":%q,"type":%q,"target":%q,"depends_on":[%q],"commercial":{"job_id":%d,"revision":%q,"notes":"Blocked edit"}}]}`, stale, kind+".edit", fmt.Sprint(receipt.Data.ID), id, job.ID, receipt.Data.Revision, blocked, kind+".edit", fmt.Sprint(receipt.Data.ID), stale, job.ID, receipt.Data.Revision)
			dependencies := commercialBatchCall(t, api, token, dependencyBody)
			assertContains(t, dependencies, `"outcome":"stale_commercial_revision"`)
			assertContains(t, dependencies, `"outcome":"failed_dependency"`)
			for operation, want := range map[string]string{stale: "stale_commercial_revision", blocked: "failed_dependency"} {
				historyCheck(t, pool.QueryRow(ctx, `SELECT receipt->>'outcome' FROM commercial_operations WHERE operation_id=$1`, operation).Scan(&outcome))
				if outcome != want {
					t.Fatalf("dependency receipt %s=%q want %q", operation, outcome, want)
				}
			}
			historyCheck(t, pool.QueryRow(ctx, "SELECT notes FROM "+kind+"s WHERE id=$1", receipt.Data.ID).Scan(&notes))
			if notes != "Ordered edit" {
				t.Fatalf("rejected descendant changed persisted notes: %q", notes)
			}
			t.Log("persisted known-success/stale/blocked dependency receipts and unchanged effect verified")
		})
	}
}
