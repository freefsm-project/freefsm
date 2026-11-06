package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func commercialSyncCall(t *testing.T, api http.Handler, token, path string, want int) authorization.SyncPage {
	t.Helper()
	r := httptest.NewRequest("GET", path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("GET %s = %d want %d: %s", path, w.Code, want, w.Body.String())
	}
	if want >= 400 && (strings.Contains(w.Body.String(), "Discovery secret") || !strings.Contains(w.Body.String(), `"purge":"all"`)) {
		t.Fatalf("non-disclosing purge required: %s", w.Body.String())
	}
	specResponse := httptest.NewRecorder()
	api.ServeHTTP(specResponse, httptest.NewRequest("GET", "/openapi.json", nil))
	var spec map[string]any
	if err := json.Unmarshal(specResponse.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://commercial-sync.test/openapi.json", spec); err != nil {
		t.Fatal(err)
	}
	name := "CommercialSyncEnvelope"
	if want >= 400 {
		name = "ErrorEnvelope"
	}
	schema, err := compiler.Compile("https://commercial-sync.test/openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(w.Body.String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("served %s: %v", name, err)
	}
	var envelope struct {
		Data authorization.SyncPage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func TestCommercialSyncInvoiceSettlementAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Settlement discovery").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Settlement discovery").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, job.ID, admin)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "settlement discovery")
	if err != nil {
		t.Fatal(err)
	}
	lifecycleCall(t, api, token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Settlement","line_items":[{"title":"Work","quantity":"1","unit_price":"100.00"}]}`, newOperationKey(), newOperationKey(), job.ID, customer.ID), 200)
	page := commercialSyncCall(t, api, token, "/sync/invoices", 200)
	id := int64(page.Records[0]["legacy_id"].(float64))
	commercialSyncTransition(t, api, token, "invoice", id, job.ID, "finalize", "invoice:invoiced")
	finalized := commercialSyncCall(t, api, token, "/sync/invoices?cursor="+*page.CompletionCursor, 200)
	if len(finalized.Records) != 1 || finalized.Records[0]["manual_category"] != "invoice:invoiced" || finalized.Records[0]["actions"].(map[string]any)["status"] != true {
		t.Fatalf("finalize not discovered: %+v", finalized)
	}
	body := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"amount":"25.00","method":"cash","date":%q}`, newOperationKey(), job.ID, time.Now().Format("2006-01-02"))
	r := httptest.NewRequest("POST", fmt.Sprintf("/invoices/%d/payments", id), strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("bounded payment: %d %s", w.Code, w.Body.String())
	}
	paid := commercialSyncCall(t, api, token, "/sync/invoices?cursor="+*finalized.CompletionCursor, 200)
	if len(paid.Records) != 1 {
		t.Fatalf("missing settlement delta: %+v", paid)
	}
	doc := paid.Records[0]
	if doc["settlement_locked"] != true || doc["manual_category"] != "invoice:invoiced" || doc["effective_category"] != "invoice:partially_paid" || doc["status_id"] == doc["effective_status_id"] || doc["revision"] == finalized.Records[0]["revision"] {
		t.Fatalf("incoherent settlement state: %+v", doc)
	}
	for _, action := range []string{"edit", "status", "finalize"} {
		if doc["actions"].(map[string]any)[action] != false {
			t.Fatalf("settlement permits %s", action)
		}
	}
	for _, v := range doc["workflow"].([]any) {
		if category := v.(map[string]any)["category"]; category == "invoice:paid" || category == "invoice:partially_paid" {
			t.Fatal("derived status selectable")
		}
	}
}

func TestCommercialSyncAuthorityLossAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		for _, loss := range []string{"pricing", "view", "action", "assignment", "archive"} {
			t.Run(kind+"/"+loss, func(t *testing.T) {
				f := associationRepair(t)
				ctx := context.Background()
				uid, role, _ := f.reader(t, []authorization.Grant{
					{Permission: kind + ".view", Scope: authorization.All},
					{Permission: kind + ".edit", Scope: authorization.Assigned},
					{Permission: "pricing.view", Scope: authorization.Assigned},
				})
				customer := f.client.Customer.Create().SetCompanyID(f.company).SetDisplayName("Discovery secret Customer").SaveX(ctx)
				job := f.client.Job.Create().SetCompanyID(f.company).SetCustomerID(customer.ID).SetJobType("Discovery secret Job").SaveX(ctx)
				assignFieldDocumentCreator(t, f.pool, job.ID, f.admin)
				f.client.JobAssignment.Create().SetJobID(job.ID).SetUserID(uid).SaveX(ctx)
				api := apiv1.NewRouter(f.pool, f.client, f.sessions)
				admin, err := f.sessions.CreateMobile(ctx, f.admin, "creator")
				if err != nil {
					t.Fatal(err)
				}
				token, err := f.sessions.CreateMobile(ctx, uid, "independent document reader")
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 2; i++ {
					lifecycleCall(t, api, admin, "POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Discovery secret","line_items":[{"title":"Captured","quantity":"1","unit_price":"42.00"}]}`, newOperationKey(), newOperationKey(), job.ID, customer.ID), 200)
				}
				path := "/sync/" + kind + "s?scope=all&page_size=1"
				first := commercialSyncCall(t, api, token, path, 200)
				if len(first.Records) != 1 || first.NextPage == nil || first.Records[0]["totals"].(map[string]any)["total"] != "42.00" {
					t.Fatalf("independent document/pricing read: %+v", first)
				}
				switch loss {
				case "pricing", "view", "action":
					permission := map[string]string{"pricing": "pricing.view", "view": kind + ".view", "action": kind + ".edit"}[loss]
					_, err = f.pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission=$2`, role, permission)
				case "assignment":
					_, err = f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1 AND job_id=$2`, uid, job.ID)
				case "archive":
					if kind == "invoice" {
						id := int64(first.Records[0]["legacy_id"].(float64))
						commercialSyncTransition(t, api, admin, kind, id, job.ID, "finalize", "invoice:invoiced")
						commercialSyncTransition(t, api, admin, kind, id, job.ID, "status", "invoice:void")
					}
					_, err = f.pool.Exec(ctx, `UPDATE `+kind+`s SET deleted_at=clock_timestamp() WHERE id=$1`, int64(first.Records[0]["legacy_id"].(float64)))
				}
				if err != nil {
					t.Fatal(err)
				}
				want := 409
				if loss == "view" {
					want = 403
				}
				commercialSyncCall(t, api, token, path+"&page="+*first.NextPage, want)
				if loss != "view" {
					fresh := commercialSyncCall(t, api, token, path, 200)
					if loss == "pricing" || loss == "assignment" {
						for _, record := range fresh.Records {
							for _, key := range []string{"line_items", "financial", "tax_rate", "totals", "arithmetic_version"} {
								if _, ok := record[key]; ok {
									t.Fatalf("lost pricing leaked %s", key)
								}
							}
						}
					}
					if loss == "action" || loss == "assignment" {
						if fresh.Records[0]["actions"].(map[string]any)["edit"] != false {
							t.Fatal("lost edit capability retained")
						}
					}
					commercialSyncCall(t, api, token, path+"&page="+*first.NextPage, 409)
				}
			})
		}
	}
}

func commercialSyncTransition(t *testing.T, api http.Handler, token, kind string, id, job int64, action, category string) {
	t.Helper()
	path := fmt.Sprintf("/%ss/%d", kind, id)
	var current struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(lifecycleCall(t, api, token, "GET", path, "", 200)), &current); err != nil {
		t.Fatal(err)
	}
	for _, v := range current.Data["workflow"].([]any) {
		choice := v.(map[string]any)
		if choice["category"] == category {
			lifecycleCall(t, api, token, "POST", path+"/"+action, fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"status_id":%.0f,"meaning_revision":%q}`, newOperationKey(), job, current.Data["revision"], choice["id"], choice["meaning_revision"]), 200)
			return
		}
	}
	t.Fatalf("missing workflow choice %s", category)
}

func TestCommercialSyncContinuityAPI(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Discovery Customer").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Discovery Job").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, job.ID, admin)
			api := apiv1.NewRouter(pool, client, sessions)
			token, err := sessions.CreateMobile(ctx, admin, "Commercial discovery")
			if err != nil {
				t.Fatal(err)
			}
			var field int64
			if err := pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type) VALUES($1,$2,'Reviewed value','text') RETURNING id`, company, kind).Scan(&field); err != nil {
				t.Fatal(err)
			}
			for _, title := range []string{"First", "Second"} {
				lifecycleCall(t, api, token, "POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":%q,"custom_fields":[{"definition_id":%d,"value":"Original value"}]}`, newOperationKey(), newOperationKey(), job.ID, customer.ID, title, field), 200)
			}
			path := "/sync/" + kind + "s?page_size=1"
			first := commercialSyncCall(t, api, token, path, 200)
			if len(first.Records) != 1 || first.NextPage == nil || first.Complete {
				t.Fatalf("first page: %+v", first)
			}
			doc := first.Records[0]
			actions, ok := doc["actions"].(map[string]any)
			if !ok || actions["edit"] != true || actions["status"] != (kind == "estimate") || actions["finalize"] != (kind == "invoice") {
				t.Fatalf("independent applicable actions: %+v", doc["actions"])
			}
			if doc["effective_status_id"] != doc["status_id"] || doc["totals"].(map[string]any)["total"] != "0.00" {
				t.Fatalf("workflow/totals: %+v", doc)
			}
			if doc["title"] != "First" || doc["revision"] == "" || doc["settlement_locked"] != false {
				t.Fatalf("commercial state: %+v", doc)
			}
			id := int64(doc["legacy_id"].(float64))
			if doc["custom_fields"].([]any)[0].(map[string]any)["value"] != "Original value" {
				t.Fatalf("editable values: %+v", doc)
			}
			lifecycleCall(t, api, token, "PATCH", fmt.Sprintf("/%ss/%d", kind, id), fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"notes":"During pagination","custom_fields":[{"definition_id":%d,"value":"Changed value"}]}`, newOperationKey(), job.ID, doc["revision"], field), 200)
			last := commercialSyncCall(t, api, token, path+"&page="+*first.NextPage, 200)
			if !last.Complete || last.CompletionCursor == nil || !last.Watermark.Equal(first.Watermark) {
				t.Fatalf("last page: %+v", last)
			}
			delta := commercialSyncCall(t, api, token, path+"&cursor="+*last.CompletionCursor, 200)
			if len(delta.Records) != 1 || delta.Records[0]["id"] != doc["id"] || delta.Records[0]["notes"] != "During pagination" || delta.Records[0]["revision"] == doc["revision"] {
				t.Fatalf("missed edit: %+v", delta)
			}
			if delta.Records[0]["custom_fields"].([]any)[0].(map[string]any)["value"] != "Changed value" {
				t.Fatalf("missed field edit: %+v", delta)
			}
			if kind == "invoice" {
				commercialSyncTransition(t, api, token, kind, id, job.ID, "finalize", "invoice:invoiced")
				paid := commercialSyncCall(t, api, token, path+"&cursor="+*delta.CompletionCursor, 200)
				if len(paid.Records) != 1 || paid.Records[0]["effective_category"] != "invoice:paid" || paid.Records[0]["manual_category"] != "invoice:invoiced" || paid.Records[0]["settlement_locked"] != false {
					t.Fatalf("zero-total projection: %+v", paid)
				}
				commercialSyncTransition(t, api, token, kind, id, job.ID, "status", "invoice:draft")
				draft := commercialSyncCall(t, api, token, path+"&cursor="+*paid.CompletionCursor, 200)
				if len(draft.Records) != 1 || draft.Records[0]["effective_category"] != "invoice:draft" {
					t.Fatalf("return Draft: %+v", draft)
				}
			} else {
				commercialSyncTransition(t, api, token, kind, id, job.ID, "status", "estimate:accepted")
				accepted := commercialSyncCall(t, api, token, path+"&cursor="+*delta.CompletionCursor, 200)
				if len(accepted.Records) != 1 || accepted.Records[0]["manual_category"] != "estimate:accepted" || accepted.Records[0]["actions"].(map[string]any)["edit"] != true {
					t.Fatalf("Estimate workflow: %+v", accepted)
				}
			}
		})
	}
}
