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

func TestFieldDocumentProtectedReference(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			for _, scenario := range []string{"creation", "archive", "binding", "required", "restore", "revoke", "quarantine", "grant", "assignment", "lost_grant"} {
				t.Run(scenario, func(t *testing.T) {
					pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
					ctx := context.Background()
					customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Encrypted creation Customer").SaveX(ctx)
					job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Encrypted creation Job").SaveX(ctx)
					assignFieldDocumentCreator(t, pool, job.ID, admin)
					other := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Unrelated authorized work").SaveX(ctx)
					assignFieldDocumentCreator(t, pool, other.ID, admin)
					item := client.Item.Create().SetCompanyID(company).SetName("Captured Item").SetType("service").SetUnitPrice(12.34).SetUnitCost(9.87).SaveX(ctx)
					if scenario == "creation" {
						_, err := pool.Exec(ctx, `UPDATE company_settings SET default_invoice_notes='Company notes',default_estimate_notes='Company notes',default_due_days=14,estimate_valid_days=7 WHERE company_id=$1`, company)
						historyCheck(t, err)
						_, err = pool.Exec(ctx, `INSERT INTO customer_document_defaults(customer_id,defaults) VALUES($1,'{"invoice_notes":null,"estimate_notes":null,"invoice_terms":"Customer terms","estimate_terms":"Customer terms","invoice_due_days":null,"estimate_valid_days":null}')`, customer.ID)
						historyCheck(t, err)
						for _, field := range [][2]string{{"text", "Default text"}, {"textarea", "Default\nparagraph"}, {"number", "-1.23"}, {"date", "2024-02-29"}, {"checkbox", "false"}, {"select", "Current"}, {"multiselect", `["Current","Other"]`}} {
							_, err := pool.Exec(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,precision,required,options,retired_options,default_value) VALUES($1,$2,$3,$3,2,true,'["Old","Current","Other"]','["Old"]',$4)`, company, kind, field[0], field[1])
							historyCheck(t, err)
						}
					}
					user := admin
					auth := authorization.New(pool)
					actor := authorization.Actor{UserID: admin}
					grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "customer.view", Scope: authorization.Context}, {Permission: "pricing.view", Scope: authorization.Assigned}, {Permission: kind + ".view", Scope: authorization.Assigned}, {Permission: kind + ".create", Scope: authorization.Assigned}, {Permission: "item.view", Scope: authorization.All}, {Permission: "pricebook.view", Scope: authorization.All}}
					var role int64
					if scenario == "grant" || scenario == "assignment" || scenario == "lost_grant" {
						u := client.User.Create().SetCompanyID(company).SetName("Scoped creator").SetEmail("encrypted-creator@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
						user = u.ID
						var err error
						role, err = auth.SaveRole(ctx, actor, 0, "Scoped encrypted creator", grants)
						historyCheck(t, err)
						historyCheck(t, auth.AssignUser(ctx, actor, user, role))
						assignFieldDocumentCreator(t, pool, job.ID, user)
						assignFieldDocumentCreator(t, pool, other.ID, user)
					}
					token, err := sessions.CreateMobile(ctx, user, "Encrypted creation reference")
					historyCheck(t, err)
					api := apiv1.NewRouter(pool, client, sessions)
					validate := serviceEntrySchemaValidator(t, api)
					var specification map[string]any
					historyCheck(t, json.Unmarshal(serviceAPIRequest(api, "", "GET", "/openapi.json", nil).Body.Bytes(), &specification))
					compiler := jsonschema.NewCompiler()
					compiler.AssertFormat()
					historyCheck(t, compiler.AddResource("https://creation.test/openapi.json", specification))
					createSchema, err := compiler.Compile("https://creation.test/openapi.json#/components/requestBodies/CommercialCreate/content/application~1json/schema")
					historyCheck(t, err)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/__fixture/exempt" {
							client.Customer.UpdateOneID(customer.ID).SetTaxExempt(r.URL.Query().Get("enabled") == "true").SaveX(ctx)
							w.WriteHeader(204)
							return
						}
						if r.URL.Path == "/__fixture/price" {
							client.Item.UpdateOneID(item.ID).SetUnitPrice(42).SaveX(ctx)
							_, err := pool.Exec(ctx, `UPDATE company_settings SET default_invoice_notes='Changed notes',default_estimate_notes='Changed notes',default_due_days=99,estimate_valid_days=99 WHERE company_id=$1`, company)
							historyCheck(t, err)
							w.WriteHeader(204)
							return
						}
						if r.URL.Path == "/__fixture/change" {
							var err error
							switch scenario {
							case "archive", "quarantine":
								_, err = pool.Exec(ctx, `UPDATE items SET is_active=false WHERE id=$1`, item.ID)
							case "binding":
								next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Different Customer").SaveX(ctx)
								_, err = pool.Exec(ctx, `UPDATE jobs SET customer_id=$1 WHERE id=$2`, next.ID, job.ID)
							case "required":
								_, err = pool.Exec(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,required) VALUES($1,$2,'New required choice','select',true)`, company, kind)
								if err == nil {
									_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET options='["Reviewed"]' WHERE company_id=$1 AND object_type=$2`, company, kind)
								}
							case "restore":
								_, err = pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`)
								if err == nil {
									var fresh string
									fresh, err = sessions.CreateMobile(ctx, admin, "Restored creation reference")
									if err == nil {
										_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
										return
									}
								}
							case "revoke":
								_, err = pool.Exec(ctx, `DELETE FROM sessions`)
							case "grant", "lost_grant":
								_, err = auth.SaveRole(ctx, actor, role, "Scoped encrypted creator", grants[:len(grants)-1], 1)
							case "assignment":
								var review authorization.DispatchReview
								review, err = auth.ReviewDispatch(ctx, actor, job.ID, authorization.DispatchPlan{})
								if err == nil {
									err = auth.ConfirmDispatch(ctx, actor, job.ID, review.ID, true)
								}
							}
							if err != nil {
								t.Error(err)
								w.WriteHeader(500)
								return
							}
							w.WriteHeader(204)
							return
						}
						creating := r.Method == "POST" && (r.URL.Path == "/estimates" || r.URL.Path == "/invoices")
						before := ""
						if creating {
							body, err := io.ReadAll(r.Body)
							historyCheck(t, err)
							value, err := jsonschema.UnmarshalJSON(bytes.NewReader(body))
							historyCheck(t, err)
							historyCheck(t, createSchema.Validate(value))
							r.Body = io.NopCloser(bytes.NewReader(body))
							before = fieldCreationEffects(t, pool, company)
						}
						response := httptest.NewRecorder()
						api.ServeHTTP(response, r)
						if response.Code >= 400 {
							validate("ErrorEnvelope", response.Body.Bytes())
							if creating && before != fieldCreationEffects(t, pool, company) {
								t.Error("Rejected creation left partial effects")
							}
						} else {
							schema := ""
							switch {
							case creating, strings.HasPrefix(r.URL.Path, "/commercial-operations/"):
								schema = "CommercialReceiptEnvelope"
							case strings.HasPrefix(r.URL.Path, "/estimates/"), strings.HasPrefix(r.URL.Path, "/invoices/"):
								schema = "CommercialReadEnvelope"
							case r.URL.Path == "/sync/jobs":
								schema = "JobSyncEnvelope"
							case r.URL.Path == "/sync/items":
								schema = "ItemSyncEnvelope"
							}
							if schema != "" {
								validate(schema, response.Body.Bytes())
							}
						}
						for key, values := range response.Header() {
							w.Header()[key] = values
						}
						w.WriteHeader(response.Code)
						_, _ = w.Write(response.Body.Bytes())
					}))
					defer server.Close()
					directory := t.TempDir()
					historyCheck(t, os.Chmod(directory, 0700))
					input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "kind": kind, "scenario": scenario, "job": fmt.Sprint(job.ID)})
					historyCheck(t, err)
					runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
					defer cancel()
					cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/document-creation-http.cjs")
					cmd.Stdin = bytes.NewReader(input)
					out, err := cmd.CombinedOutput()
					t.Logf("encrypted creation reference: %s", out)
					historyCheck(t, err)
				})
			}
		})
	}
}
