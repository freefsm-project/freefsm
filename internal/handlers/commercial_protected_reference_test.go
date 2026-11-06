package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCommercialProtectedReferenceAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		for _, scenario := range []string{"conflict", "restore", "revoke", "lifecycle", "failed_comparison", "scope_loss"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Protected commercial Customer").SaveX(ctx)
				job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Protected commercial Job").SaveX(ctx)
				assignFieldDocumentCreator(t, pool, job.ID, admin)
				token, err := sessions.CreateMobile(ctx, admin, "Protected commercial reference")
				historyCheck(t, err)
				api := apiv1.NewRouter(pool, client, sessions)
				lifecycleCall(t, api, token, "POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Protected commercial document"}`, newOperationKey(), newOperationKey(), job.ID, customer.ID), 200)
				auth := authorization.New(pool)
				var role int64
				if scenario == "scope_loss" {
					u := client.User.Create().SetCompanyID(company).SetName("Scoped commercial reference").SetEmail("protected-commercial@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
					role, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Scoped commercial reference", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: kind + ".view", Scope: authorization.Assigned}, {Permission: kind + ".edit", Scope: authorization.Assigned}, {Permission: "pricing.view", Scope: authorization.Assigned}})
					historyCheck(t, err)
					historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
					assignFieldDocumentCreator(t, pool, job.ID, u.ID)
					token, err = sessions.CreateMobile(ctx, u.ID, "Scoped commercial reference")
					historyCheck(t, err)
				}
				validate := serviceEntrySchemaValidator(t, api)
				var spec map[string]any
				historyCheck(t, json.Unmarshal(serviceAPIRequest(api, "", "GET", "/openapi.json", nil).Body.Bytes(), &spec))
				compiler := jsonschema.NewCompiler()
				compiler.AssertFormat()
				historyCheck(t, compiler.AddResource("https://protected-commercial.test/openapi.json", spec))
				requests := map[string]*jsonschema.Schema{}
				for _, name := range []string{"CommercialEdit", "CommercialLifecycle"} {
					schema, err := compiler.Compile("https://protected-commercial.test/openapi.json#/components/requestBodies/" + name + "/content/application~1json/schema")
					historyCheck(t, err)
					requests[name] = schema
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/__fixture/change" {
						if scenario == "scope_loss" {
							_, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Scoped commercial reference", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}}, 1)
							historyCheck(t, err)
							w.WriteHeader(204)
						} else if scenario == "restore" {
							_, err := pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`)
							historyCheck(t, err)
							fresh, err := sessions.CreateMobile(ctx, admin, "Restored commercial reference")
							historyCheck(t, err)
							_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
						} else {
							_, err := pool.Exec(ctx, `DELETE FROM sessions`)
							historyCheck(t, err)
							w.WriteHeader(204)
						}
						return
					}
					if r.Method != "GET" && strings.HasPrefix(r.URL.Path, "/"+kind+"s/") {
						body, err := io.ReadAll(r.Body)
						historyCheck(t, err)
						value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
						historyCheck(t, err)
						name := "CommercialLifecycle"
						if r.Method == "PATCH" {
							name = "CommercialEdit"
						}
						historyCheck(t, requests[name].Validate(value))
						r.Body = io.NopCloser(bytes.NewReader(body))
					}
					response := httptest.NewRecorder()
					api.ServeHTTP(response, r)
					schema := ""
					switch {
					case response.Code >= 400:
						schema = "ErrorEnvelope"
					case strings.HasPrefix(r.URL.Path, "/sync/"+kind+"s"):
						schema = "CommercialSyncEnvelope"
					case strings.HasPrefix(r.URL.Path, "/commercial-operations/"), r.Method != "GET" && strings.HasPrefix(r.URL.Path, "/"+kind+"s/"):
						schema = "CommercialReceiptEnvelope"
					case strings.HasPrefix(r.URL.Path, "/"+kind+"s/"):
						schema = "CommercialReadEnvelope"
					}
					if schema != "" {
						validate(schema, response.Body.Bytes())
					}
					for k, v := range response.Header() {
						w.Header()[k] = v
					}
					w.WriteHeader(response.Code)
					_, _ = w.Write(response.Body.Bytes())
				}))
				defer server.Close()
				directory := t.TempDir()
				historyCheck(t, os.Chmod(directory, 0700))
				input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "kind": kind, "scenario": scenario})
				historyCheck(t, err)
				runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
				defer cancel()
				cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/commercial-protected-http.cjs")
				cmd.Stdin = bytes.NewReader(input)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("protected reference: %v\n%s", err, out)
				}
				t.Log(string(out))
				if scenario == "lifecycle" {
					var effects int
					historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM invoice_payments)+(SELECT count(*) FROM payment_invoice_allocations)+(SELECT count(*) FROM credit_applications)`).Scan(&effects))
					if effects != 0 {
						t.Fatalf("workflow fabricated financial history: %d", effects)
					}
				}
			})
		}
	}
}
