package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCommercialProtectedReferenceAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		scenarios := []string{"conflict", "restore", "revoke", "lifecycle", "failed_comparison", "scope_loss", "revision_aba", "revision_fields", "revision_status", "revision_finalize", "collaboration", "nonzero_lifecycle", "changed_fields", "semantic_category", "semantic_aba", "semantic_replacement", "semantic_rename", "settlement_allocation", "settlement_credit", "authority_pricing", "authority_edit", "authority_assignment", "authority_archive", "batch_success", "batch_recovery", "batch_transient", "batch_scope_loss", "batch_restore", "batch_revoke", "batch_lifecycle", "batch_storage", "batch_purge_storage", "batch_restore_storage"}
		scenarios = append(scenarios, "authority_pricebook", "authority_pricing_snapshot", "input_archive", "input_price", "transient", "purge_storage", "restore_storage")
		if kind == "invoice" {
			for _, source := range []string{"allocation", "credit"} {
				for _, action := range []string{"edit", "void"} {
					for _, order := range []string{"settlement_first", "commercial_first"} {
						scenarios = append(scenarios, "race_"+source+"_"+action+"_"+order)
					}
				}
			}
		}
		for _, scenario := range scenarios {
			if scenario == "revision_finalize" && kind != "invoice" {
				continue
			}
			if strings.HasPrefix(scenario, "semantic_") && kind != "estimate" {
				continue
			}
			if strings.HasPrefix(scenario, "settlement_") && kind != "invoice" {
				continue
			}
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Protected commercial Customer").SaveX(ctx)
				job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Protected commercial Job").SaveX(ctx)
				assignFieldDocumentCreator(t, pool, job.ID, admin)
				token, err := sessions.CreateMobile(ctx, admin, "Protected commercial reference")
				historyCheck(t, err)
				api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100})
				creation := map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": job.ID, "customer_id": customer.ID, "title": "Protected commercial document"}
				if scenario == "nonzero_lifecycle" || strings.HasPrefix(scenario, "settlement_") || strings.HasPrefix(scenario, "race_") {
					creation["line_items"] = []map[string]any{{"title": "Captured work", "quantity": "1", "unit_price": "100.00"}}
					if kind == "estimate" {
						for _, category := range []string{"draft", "estimate", "sent", "accepted", "rejected", "completed"} {
							_, err := statusflow.New(pool).Create(ctx, statusflow.Actor{ID: admin, CompanyID: company}, statusflow.CreateRequest{Type: statusflow.Estimate, Name: "Configured " + category, Category: statusflow.CategoryKey("estimate:" + category)})
							historyCheck(t, err)
						}
					}
				}
				created := matrixState(t, lifecycleCall(t, api, token, "POST", "/"+kind+"s", matrixBody(t, creation), 200))
				path := fmt.Sprintf("/%ss/%d", kind, created.ID)
				workflow := statusflow.New(pool)
				workflowActor := statusflow.Actor{ID: admin, CompanyID: company}
				var semanticID int64
				if strings.HasPrefix(scenario, "semantic_") {
					choice, err := workflow.Create(ctx, workflowActor, statusflow.CreateRequest{Type: statusflow.Estimate, Name: "Reviewed semantic choice", Category: statusflow.EstimateAccepted})
					historyCheck(t, err)
					semanticID = choice.ID
				}
				fields := map[string]int64{}
				var itemID int64
				if scenario == "authority_pricebook" || strings.HasPrefix(scenario, "input_") {
					item := client.Item.Create().SetCompanyID(company).SetName("Reviewed input").SetType("service").SetUnitPrice(10).SaveX(ctx)
					itemID = item.ID
				}
				if (scenario == "revision_status" && kind == "invoice") || strings.HasPrefix(scenario, "settlement_") || strings.HasPrefix(scenario, "race_") {
					matrixTransition(t, api, token, path, job.ID, matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200)), "invoice:invoiced", "finalize", 200)
				}
				if strings.HasPrefix(scenario, "batch_") {
					for i := 0; i < 2; i++ {
						lifecycleCall(t, api, token, "POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Protected batch document"}`, newOperationKey(), newOperationKey(), job.ID, customer.ID), 200)
					}
				}
				auth := authorization.New(pool)
				var role int64
				var scopedUser int64
				if scenario == "scope_loss" || scenario == "batch_scope_loss" || strings.HasPrefix(scenario, "authority_") {
					u := client.User.Create().SetCompanyID(company).SetName("Scoped commercial reference").SetEmail("protected-commercial@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
					scopedUser = u.ID
					role, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Scoped commercial reference", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: kind + ".view", Scope: authorization.Assigned}, {Permission: kind + ".edit", Scope: authorization.Assigned}, {Permission: "pricing.view", Scope: authorization.Assigned}, {Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All}})
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
					if r.URL.Path == "/__fixture/attribution" {
						var count int
						historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE object_type=$1 AND object_id=$2 AND action='status_transitioned'`, kind, created.ID).Scan(&count))
						_ = json.NewEncoder(w).Encode(map[string]int{"transitions": count})
						return
					}
					if r.URL.Path == "/__fixture/input" {
						if scenario == "input_archive" {
							cookie := sessionCookie(t, ctx, sessions, admin)
							if response := jobRepairRawPost(web, cookie, fmt.Sprintf("/items/%d/delete", itemID), nil); response.Code != 303 {
								t.Errorf("archive input: %d %s", response.Code, response.Body.String())
							}
						} else {
							client.Item.UpdateOneID(itemID).SetUnitPrice(42).SaveX(ctx)
						}
						w.WriteHeader(204)
						return
					}
					if r.URL.Path == "/__fixture/settle" {
						svc := settlement.New(pool)
						date := settlement.Date(time.Now().Format("2006-01-02"))
						actor := settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
						if scenario == "settlement_allocation" {
							_, err := svc.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: created.ID, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: date})
							historyCheck(t, err)
						} else {
							_, err := svc.RecordCustomerPayment(ctx, authorization.Actor{UserID: admin, Channel: "web"}, customer.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: date})
							historyCheck(t, err)
							var credit uuid.UUID
							historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE customer_id=$1`, customer.ID).Scan(&credit))
							_, err = svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: created.ID, CreditID: credit, RequestedCents: 2500, EffectiveDate: date})
							historyCheck(t, err)
						}
						w.WriteHeader(204)
						return
					}
					if r.URL.Path == "/__fixture/semantics" {
						switch scenario {
						case "semantic_category", "semantic_aba":
							historyCheck(t, workflow.Move(ctx, workflowActor, statusflow.MoveRequest{StatusID: semanticID, Category: statusflow.EstimateRejected}))
							if scenario == "semantic_aba" {
								historyCheck(t, workflow.Move(ctx, workflowActor, statusflow.MoveRequest{StatusID: semanticID, Category: statusflow.EstimateAccepted}))
							}
						case "semantic_replacement":
							replacement, err := workflow.Create(ctx, workflowActor, statusflow.CreateRequest{Type: statusflow.Estimate, Name: "Replacement", Category: statusflow.EstimateAccepted})
							historyCheck(t, err)
							historyCheck(t, workflow.Delete(ctx, workflowActor, semanticID, replacement.ID))
							_, err = workflow.Update(ctx, workflowActor, replacement.ID, "Reviewed semantic choice", "#123456")
							historyCheck(t, err)
							semanticID = replacement.ID
						case "semantic_rename":
							_, err := workflow.Update(ctx, workflowActor, semanticID, "Cosmetic rename", "#123456")
							historyCheck(t, err)
						default:
							t.Errorf("unexpected semantic fixture %s", scenario)
						}
						_ = json.NewEncoder(w).Encode(map[string]int64{"id": semanticID})
						return
					}
					if r.URL.Path == "/__fixture/fields" {
						switch r.URL.Query().Get("action") {
						case "create":
							for _, typ := range []string{"text", "textarea", "number", "date", "checkbox", "select", "multiselect"} {
								var id int64
								options := `[]`
								if typ == "select" || typ == "multiselect" {
									options = `["Old","Current"]`
								}
								historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,precision,options) VALUES($1,$2,$3,$3,2,$4::jsonb) RETURNING id`, company, kind, typ, options).Scan(&id))
								fields[typ] = id
							}
							var id int64
							historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,required) VALUES($1,$2,'New required','text',true) RETURNING id`, company, kind).Scan(&id))
							fields["required"] = id
						case "retire":
							_, err := pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=true WHERE id=$1;`, fields["number"])
							historyCheck(t, err)
							_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET retired_options='["Old"]' WHERE id IN ($1,$2)`, fields["select"], fields["multiselect"])
							historyCheck(t, err)
						case "reactivate":
							_, err := pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=false,precision=1 WHERE id=$1`, fields["number"])
							historyCheck(t, err)
						default:
							t.Error("unexpected field fixture action")
						}
						_ = json.NewEncoder(w).Encode(fields)
						return
					}
					if r.URL.Path == "/__fixture/drift" {
						read := func() commercialMatrixState {
							return matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200))
						}
						switch scenario {
						case "revision_aba":
							for _, notes := range []string{"Intervening notes", ""} {
								lifecycleCall(t, api, token, "PATCH", path, matrixBody(t, map[string]any{"operation_id": newOperationKey(), "job_id": job.ID, "revision": read().Revision, "notes": notes}), 200)
							}
						case "revision_fields":
							var field int64
							historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type) VALUES($1,$2,'Concurrent field','text') RETURNING id`, company, kind).Scan(&field))
							lifecycleCall(t, api, token, "PATCH", path, matrixBody(t, map[string]any{"operation_id": newOperationKey(), "job_id": job.ID, "revision": read().Revision, "custom_fields": []map[string]any{{"definition_id": field, "value": "Concurrent value"}}}), 200)
						case "revision_status", "revision_finalize":
							action, category := "status", "estimate:accepted"
							if kind == "invoice" {
								action, category = "finalize", "invoice:invoiced"
								if scenario == "revision_status" {
									action, category = "status", "invoice:sent"
								}
							}
							matrixTransition(t, api, token, path, job.ID, read(), category, action, 200)
						case "collaboration":
							cookie := sessionCookie(t, ctx, sessions, admin)
							documentTransactionRequest(t, web, cookie, "POST", path+"/comments", url.Values{"content": {"Independent collaboration"}}, 200)
							commercialUpload(t, web, cookie, kind, created.ID)
						default:
							t.Errorf("unexpected drift scenario %s", scenario)
						}
						w.WriteHeader(204)
						return
					}
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
						if strings.HasPrefix(scenario, "authority_") {
							switch scenario {
							case "authority_pricing", "authority_pricing_snapshot", "authority_edit", "authority_pricebook":
								permission := "pricing.view"
								if scenario == "authority_edit" {
									permission = kind + ".edit"
								}
								if scenario == "authority_pricebook" {
									permission = "pricebook.view"
								}
								_, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, permission)
								historyCheck(t, err)
							case "authority_assignment":
								_, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1 AND job_id=$2`, scopedUser, job.ID)
								historyCheck(t, err)
							case "authority_archive":
								_, err := pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, job.ID)
								historyCheck(t, err)
							}
							w.WriteHeader(204)
						} else if scenario == "scope_loss" || scenario == "batch_scope_loss" {
							_, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Scoped commercial reference", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}}, 1)
							historyCheck(t, err)
							w.WriteHeader(204)
						} else if scenario == "restore" || scenario == "restore_storage" || scenario == "batch_restore" || scenario == "batch_restore_storage" {
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
					if r.Header.Get("X-Settlement-Race") == "yes" {
						response = commercialProtectedSettlementRace(t, pool, api, r, token, path, company, admin, customer.ID, created.ID, job.ID, strings.Contains(scenario, "_credit_"), strings.HasSuffix(scenario, "_settlement_first"))
					} else {
						api.ServeHTTP(response, r)
					}
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
						found := false
						for template, value := range spec["paths"].(map[string]any) {
							parts, actual := strings.Split(template, "/"), strings.Split(r.URL.Path, "/")
							if len(parts) != len(actual) {
								continue
							}
							match := true
							for i, part := range parts {
								if !strings.HasPrefix(part, "{") && part != actual[i] {
									match = false
								}
							}
							if match {
								if operation, ok := value.(map[string]any)[strings.ToLower(r.Method)].(map[string]any); ok {
									found = true
									responses := operation["responses"].(map[string]any)
									_, explicit := responses[fmt.Sprint(response.Code)]
									_, fallback := responses["default"]
									if !explicit && !fallback {
										t.Errorf("undocumented %s %s status %d", r.Method, template, response.Code)
									}
								}
							}
						}
						if !found {
							t.Errorf("undocumented operation %s %s", r.Method, r.URL.Path)
						}
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
				input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "kind": kind, "scenario": scenario, "semanticID": semanticID, "itemID": itemID})
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
				if scenario == "lifecycle" || scenario == "nonzero_lifecycle" || scenario == "batch_lifecycle" {
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
