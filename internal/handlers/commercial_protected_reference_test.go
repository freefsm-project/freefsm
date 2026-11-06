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
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCommercialProtectedReferenceAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		for _, scenario := range []string{"conflict", "restore", "revoke", "lifecycle", "failed_comparison", "scope_loss", "batch_success", "batch_recovery", "batch_transient", "batch_scope_loss", "batch_restore", "batch_revoke", "batch_lifecycle", "batch_storage", "batch_purge_storage", "batch_restore_storage"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Protected commercial Customer").SaveX(ctx)
				job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Protected commercial Job").SaveX(ctx)
				assignFieldDocumentCreator(t, pool, job.ID, admin)
				token, err := sessions.CreateMobile(ctx, admin, "Protected commercial reference")
				historyCheck(t, err)
				api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100})
				lifecycleCall(t, api, token, "POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Protected commercial document"}`, newOperationKey(), newOperationKey(), job.ID, customer.ID), 200)
				if strings.HasPrefix(scenario, "batch_") {
					for i := 0; i < 2; i++ {
						lifecycleCall(t, api, token, "POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Protected batch document"}`, newOperationKey(), newOperationKey(), job.ID, customer.ID), 200)
					}
				}
				auth := authorization.New(pool)
				var role int64
				if scenario == "scope_loss" || scenario == "batch_scope_loss" {
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
					if r.URL.Path == "/__fixture/fault" {
						var in struct{ ID string }
						historyCheck(t, json.NewDecoder(r.Body).Decode(&in))
						// UUID is parsed by PostgreSQL, never interpolated into SQL.
						_, err := pool.Exec(ctx, `CREATE TABLE batch_fault (id uuid); CREATE FUNCTION protected_batch_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation_id IN (SELECT id FROM batch_fault) THEN RAISE EXCEPTION 'protected batch receipt failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER protected_batch_failure BEFORE INSERT ON commercial_operations FOR EACH ROW EXECUTE FUNCTION protected_batch_failure()`)
						historyCheck(t, err)
						_, err = pool.Exec(ctx, `INSERT INTO batch_fault VALUES ($1)`, in.ID)
						historyCheck(t, err)
						w.WriteHeader(204)
						return
					}
					if r.URL.Path == "/__fixture/clear" {
						_, err := pool.Exec(ctx, `DELETE FROM batch_fault`)
						historyCheck(t, err)
						w.WriteHeader(204)
						return
					}
					if r.URL.Path == "/__fixture/change" {
						if scenario == "scope_loss" || scenario == "batch_scope_loss" {
							_, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Scoped commercial reference", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}}, 1)
							historyCheck(t, err)
							w.WriteHeader(204)
						} else if scenario == "restore" || scenario == "batch_restore" || scenario == "batch_restore_storage" {
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
					if r.URL.Path == "/sync/operations" {
						raw, err := io.ReadAll(r.Body)
						historyCheck(t, err)
						validate("OperationBatch", raw)
						if r.Header.Get("X-Batch-Interrupt") == "yes" {
							// Existing reference fault seam: commit the real prefix,
							// omit remaining dispatch and sever the HTTP response.
							var body map[string][]json.RawMessage
							historyCheck(t, json.Unmarshal(raw, &body))
							body["operations"] = body["operations"][:1]
							raw, err = json.Marshal(body)
							historyCheck(t, err)
						}
						r.Body = io.NopCloser(bytes.NewReader(raw))
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
					if r.URL.Path == "/sync/operations" {
						name := "OperationBatchEnvelope"
						if response.Code >= 400 {
							name = "ErrorEnvelope"
						}
						validate(name, response.Body.Bytes())
						operation := spec["paths"].(map[string]any)["/sync/operations"].(map[string]any)["post"].(map[string]any)
						if _, ok := operation["responses"].(map[string]any)[fmt.Sprint(response.Code)]; !ok {
							t.Errorf("undocumented batch status %d", response.Code)
						}
						if r.Header.Get("X-Batch-Interrupt") == "yes" {
							if response.Code != 200 {
								t.Errorf("prefix status %d: %s", response.Code, response.Body.String())
							}
							conn, _, err := w.(http.Hijacker).Hijack()
							historyCheck(t, err)
							historyCheck(t, conn.Close())
							return
						}
					}
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
				harness := "../api/v1/testdata/commercial-protected-http.cjs"
				if strings.HasPrefix(scenario, "batch_") {
					harness = "../api/v1/testdata/commercial-batch-protected-http.cjs"
				}
				cmd := exec.CommandContext(runCtx, "node", harness)
				cmd.Stdin = bytes.NewReader(input)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("protected reference: %v\n%s", err, out)
				}
				t.Log(string(out))
				if scenario == "lifecycle" || scenario == "batch_lifecycle" {
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
