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
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestConversionActionDiscoveryAPI(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Preview shared"}, "object_type": {"estimate"}, "field_type": {"text"}, "conversion_key": {"preview-shared"}}, 303)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Preview target"}, "object_type": {"invoice"}, "field_type": {"text"}, "conversion_key": {"preview-shared"}}, 303)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Conversion discovery").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Conversion discovery").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, job.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Conversion discovery")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path string, in any, status int) map[string]any {
		return conversionAPICall(t, api, token, method, path, in, status)
	}
	estimate := call("POST", "/estimates", map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": job.ID, "customer_id": customer.ID, "title": "Reviewed conversion"}, 200)
	path := fmt.Sprintf("/estimates/%.0f", estimate["id"])
	state := call("GET", path, nil, 200)
	preview := call("GET", path+"/conversion-preview", nil, 200)
	specResponse := httptest.NewRecorder()
	api.ServeHTTP(specResponse, httptest.NewRequest("GET", "/openapi.json", nil))
	var spec map[string]any
	if err := json.Unmarshal(specResponse.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://conversion.test/openapi.json", spec); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://conversion.test/openapi.json#/components/schemas/ConversionPreviewEnvelope")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]any{"data": preview})
	if err != nil {
		t.Fatal(err)
	}
	value, err := jsonschema.UnmarshalJSON(strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatal("served preview violates OpenAPI", err)
	}
	value.(map[string]any)["data"].(map[string]any)["invented_source_history"] = true
	if err := schema.Validate(value); err == nil {
		t.Fatal("preview schema is not closed")
	}
	actions := state["actions"].(map[string]any)
	if actions["convert"] != true || actions["revert"] != false {
		t.Fatalf("Estimate conversion discovery: %v", actions)
	}
	before := commercialSyncCall(t, api, token, "/sync/estimates", 200)
	if len(before.Records) != 1 || before.Records[0]["actions"].(map[string]any)["convert"] != true {
		t.Fatalf("conversion not synchronized: %+v", before)
	}
	converted := call("POST", path+"/convert", map[string]any{"operation_id": newOperationKey(), "job_id": job.ID, "revision": state["revision"]}, 200)
	invoicePath := fmt.Sprintf("/invoices/%.0f", converted["id"])
	state = call("GET", invoicePath, nil, 200)
	actions = state["actions"].(map[string]any)
	if actions["convert"] != false || actions["revert"] != true || state["cycle_id"] != converted["cycle_id"] {
		t.Fatalf("Invoice exact-cycle discovery: %v", state)
	}
	call("GET", path, nil, 404)
	// Existing whole-cache invalidation protects inaccessible original-parent
	// content; conversion does not weaken that contract to retain tombstones.
	commercialSyncCall(t, api, token, "/sync/estimates?cursor="+*before.CompletionCursor, 409)
	removed := commercialSyncCall(t, api, token, "/sync/estimates", 200)
	if len(removed.Records) != 0 {
		t.Fatalf("hidden source not removed: %+v", removed)
	}
	invoices := commercialSyncCall(t, api, token, "/sync/invoices", 200)
	if len(invoices.Records) != 1 || invoices.Records[0]["cycle_id"] != converted["cycle_id"] || invoices.Records[0]["actions"].(map[string]any)["revert"] != true {
		t.Fatalf("exact cycle not synchronized: %+v", invoices)
	}
	call("POST", invoicePath+"/revert-to-estimate", map[string]any{"operation_id": newOperationKey(), "job_id": job.ID, "revision": state["revision"], "cycle_id": state["cycle_id"]}, 200)
	call("GET", invoicePath, nil, 404)
	state = call("GET", path, nil, 200)
	if state["actions"].(map[string]any)["convert"] != true {
		t.Fatal("restored Estimate lost conversion discovery", state)
	}
	restored := commercialSyncCall(t, api, token, "/sync/estimates?cursor="+*removed.CompletionCursor, 200)
	if len(restored.Records) != 1 || restored.Records[0]["id"] != before.Records[0]["id"] {
		t.Fatalf("restored source identity changed: %+v", restored)
	}
	commercialSyncCall(t, api, token, "/sync/invoices?cursor="+*invoices.CompletionCursor, 409)
	tombstone := commercialSyncCall(t, api, token, "/sync/invoices", 200)
	if len(tombstone.Records) != 0 {
		t.Fatalf("Invoice tombstone exposed: %+v", tombstone)
	}

	// Independent View does not imply Convert or target creation authority.
	auth := authorization.New(pool)
	reader := client.User.Create().SetCompanyID(company).SetEmail("conversion-discovery@test").SetName("Reader").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Conversion reader", []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, reader.ID, role); err != nil {
		t.Fatal(err)
	}
	readerToken, err := sessions.CreateMobile(ctx, reader.ID, "Independent reader")
	if err != nil {
		t.Fatal(err)
	}
	read := conversionAPICall(t, api, readerToken, "GET", path, nil, 200)
	if read["actions"].(map[string]any)["convert"] != false {
		t.Fatal("View implied conversion authority", read)
	}
	conversionAPICall(t, api, readerToken, "POST", path+"/convert", map[string]any{"operation_id": newOperationKey(), "job_id": job.ID, "revision": read["revision"]}, 403)
}
