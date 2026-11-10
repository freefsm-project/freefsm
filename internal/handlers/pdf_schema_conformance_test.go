package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestPDFServedSchemaConformanceHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Schema customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Schema Job").SaveX(ctx)
	invoice := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Reviewed schema").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "PDF schema")
	historyCheck(t, err)
	served := httptest.NewRecorder()
	api.ServeHTTP(served, httptest.NewRequest("GET", "/openapi.json", nil))
	if served.Code != 200 {
		t.Fatal(served.Code)
	}
	var spec map[string]any
	historyCheck(t, json.Unmarshal(served.Body.Bytes(), &spec))
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const resource = "https://pdf-contract.test/openapi.json"
	historyCheck(t, compiler.AddResource(resource, spec))
	validate := func(name string, value any, valid bool) {
		t.Helper()
		schema, err := compiler.Compile(resource + "#/components/schemas/" + name)
		historyCheck(t, err)
		err = schema.Validate(value)
		if (err == nil) != valid {
			t.Fatalf("%s valid=%v: %v; value=%v", name, valid, err, value)
		}
	}
	call := func(method, path, body, schema string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		// Follow path-item aliases in the served contract, and validate the actual
		// status-specific request/response schema rather than only named components.
		resolve := func(pointer string) map[string]any {
			var value any = spec
			for _, part := range strings.Split(strings.TrimPrefix(pointer, "#/"), "/") {
				part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
				value = value.(map[string]any)[part]
			}
			return value.(map[string]any)
		}
		var operation map[string]any
		for template, item := range spec["paths"].(map[string]any) {
			parts, actual := strings.Split(template, "/"), strings.Split(r.URL.Path, "/")
			if len(parts) != len(actual) {
				continue
			}
			match := true
			for n, part := range parts {
				match = match && (strings.HasPrefix(part, "{") || part == actual[n])
			}
			if !match {
				continue
			}
			definition := item.(map[string]any)
			if ref, ok := definition["$ref"].(string); ok {
				definition = resolve(ref)
			}
			operation = definition[strings.ToLower(method)].(map[string]any)
			break
		}
		if operation == nil {
			t.Fatalf("undocumented operation %s %s", method, path)
		}
		response, ok := operation["responses"].(map[string]any)[fmt.Sprint(status)].(map[string]any)
		if !ok {
			t.Fatalf("missing explicit %d response for %s", status, path)
		}
		if ref, ok := response["$ref"].(string); ok {
			response = resolve(ref)
		}
		media := "application/json"
		if schema == "binary" {
			media = "application/pdf"
		}
		wireSchema := response["content"].(map[string]any)[media].(map[string]any)["schema"].(map[string]any)
		if schema != "binary" && wireSchema["$ref"] != "#/components/schemas/"+schema {
			t.Fatalf("response schema not wired: %v", wireSchema)
		}
		if method == "POST" && status == 200 {
			request := operation["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
			var value any
			historyCheck(t, json.Unmarshal([]byte(body), &value))
			validate(strings.TrimPrefix(request["$ref"].(string), "#/components/schemas/"), value, true)
		}
		if schema == "binary" {
			if w.Header().Get("Content-Type") != "application/pdf" || !bytes.HasPrefix(w.Body.Bytes(), []byte("%PDF-")) {
				t.Fatal("not a PDF response")
			}
			return nil
		}
		var value map[string]any
		historyCheck(t, json.Unmarshal(w.Body.Bytes(), &value))
		validate(schema, value, true)
		return value
	}
	path := fmt.Sprintf("/invoices/%d/pdf", invoice.ID)
	source := call("GET", path+"/source", "", "PDFSourceEnvelope", 200)["data"].(map[string]any)
	call("GET", path+"/versions", "", "PDFVersionsEnvelope", 200)
	key := newOperationKey()
	intent := map[string]any{"operation_id": key, "job_id": j.ID, "source_token": source["source_token"], "depends_on": []string{}, "occurred_at": "2026-01-01T00:00:00Z"}
	raw := matrixBody(t, intent)
	var normalized any
	historyCheck(t, json.Unmarshal([]byte(raw), &normalized))
	validate("PDFGenerationIntent", normalized, true)
	accepted := call("POST", path+"/generate", raw, "PDFAcceptedEnvelope", 200)
	replay := call("POST", path+"/generate", raw, "PDFAcceptedEnvelope", 200)
	if matrixBody(t, accepted) != matrixBody(t, replay) {
		t.Fatal("replay changed effect")
	}
	call("GET", "/pdf-operations/"+key, "", "PDFReceiptEnvelope", 200)
	version := accepted["data"].(map[string]any)["version_id"]
	download := fmt.Sprintf("%s/versions/%.0f", path, version)
	call("GET", download, "", "binary", 200)
	versions := call("GET", path+"/versions", "", "PDFVersionsEnvelope", 200)["data"].(map[string]any)
	if versions["default_version"] != version {
		t.Fatal("default omitted accepted version")
	}
	intent["extra"] = true
	bad := matrixBody(t, intent)
	historyCheck(t, json.Unmarshal([]byte(bad), &normalized))
	validate("PDFGenerationIntent", normalized, false)
	call("POST", path+"/generate", bad, "ErrorEnvelope", 400)
	delete(intent, "extra")
	intent["source_token"] = strings.Repeat("a", 64)
	call("POST", path+"/generate", matrixBody(t, intent), "ErrorEnvelope", 409)
	client.Invoice.UpdateOne(invoice).SetNotes("Changed reviewed content").SaveX(ctx)
	call("GET", download, "", "ErrorEnvelope", 409)
	call("GET", download+"?confirm_stale=1", "", "binary", 200)
	intent["source_token"] = source["source_token"]
	rejected := newOperationKey()
	intent["operation_id"] = rejected
	call("POST", path+"/generate", matrixBody(t, intent), "ErrorEnvelope", 409)
	terminal := call("GET", "/pdf-operations/"+rejected, "", "PDFReceiptEnvelope", 200)
	terminal["data"].(map[string]any)["version_id"] = version
	validate("PDFReceiptEnvelope", terminal, false)
	delete(accepted["data"].(map[string]any), "version_id")
	validate("PDFReceiptEnvelope", accepted, false)
	// Format assertions must reject malformed UUIDs and timestamps, not merely
	// annotate them. These malformed intents also reject at the HTTP boundary.
	for _, field := range []string{"operation_id", "occurred_at"} {
		original := intent[field]
		intent[field] = "not-a-" + field
		invalid := matrixBody(t, intent)
		historyCheck(t, json.Unmarshal([]byte(invalid), &normalized))
		validate("PDFGenerationIntent", normalized, false)
		status := 400
		if field == "operation_id" {
			status = 422
		}
		call("POST", path+"/generate", invalid, "ErrorEnvelope", status)
		intent[field] = original
	}
	estimate := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Estimate schema").SetValidThrough(time.Now()).SetLineItems(`[{"item_id":0,"title":"Service","description":"Reviewed work","quantity":2,"unit_price":12.5,"taxable":false,"tax_rate":"0","discount":0,"surcharge":0}]`).SaveX(ctx)
	estimatePath := fmt.Sprintf("/estimates/%d/pdf", estimate.ID)
	estimateSource := call("GET", estimatePath+"/source", "", "PDFSourceEnvelope", 200)["data"].(map[string]any)
	estimateIntent := map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "source_token": estimateSource["source_token"], "depends_on": []string{key}}
	estimateAccepted := call("POST", estimatePath+"/generate", matrixBody(t, estimateIntent), "PDFAcceptedEnvelope", 200)["data"].(map[string]any)
	call("GET", estimatePath+"/versions", "", "PDFVersionsEnvelope", 200)
	_, err = pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
	historyCheck(t, err)
	deliveryPath := fmt.Sprintf("/estimates/%d/deliveries", estimate.ID)
	message := map[string]any{"pdf_version_id": estimateAccepted["version_id"], "confirm_stale_pdf": false, "to": []string{"recipient@example.test"}, "cc": []string{}, "bcc": []string{}, "subject": "Reviewed PDF", "text_body": "Message", "html_body": "", "extra_file_ids": []int64{}}
	var review map[string]any
	reviewRequest := httptest.NewRequest("POST", deliveryPath+"/review", strings.NewReader(matrixBody(t, message)))
	reviewRequest.Header.Set("Authorization", "Bearer "+token)
	reviewRequest.Header.Set("Content-Type", "application/json")
	reviewResponse := httptest.NewRecorder()
	api.ServeHTTP(reviewResponse, reviewRequest)
	if reviewResponse.Code != 200 {
		t.Fatal(reviewResponse.Body.String())
	}
	historyCheck(t, json.Unmarshal(reviewResponse.Body.Bytes(), &review))
	message["job_id"] = j.ID
	message["review_token"] = review["data"].(map[string]any)["review_token"]
	for _, dependency := range []struct{ id, outcome string }{{newOperationKey(), "invalid_dependency"}, {rejected, "dependency_failed"}, {estimateIntent["operation_id"].(string), "accepted"}} {
		message["operation_id"] = newOperationKey()
		message["pdf_operation_id"] = dependency.id
		encoded := matrixBody(t, message)
		historyCheck(t, json.Unmarshal([]byte(encoded), &normalized))
		validate("ReviewedDelivery", normalized, true)
		status, schema := 409, "ErrorEnvelope"
		if dependency.outcome == "accepted" {
			status, schema = 200, "DeliveryReceiptEnvelope"
		}
		call("POST", deliveryPath, encoded, schema, status)
		receipt := call("GET", "/delivery-operations/"+message["operation_id"].(string), "", "DeliveryReceiptEnvelope", 200)
		if receipt["data"].(map[string]any)["outcome"] != dependency.outcome {
			t.Fatalf("unexpected prerequisite receipt: %v", receipt)
		}
	}
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "PDF view only", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}})
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetEmail("pdf-schema-view@test").SetName("PDF reader").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role))
	token, err = sessions.CreateMobile(ctx, user.ID, "PDF denied")
	historyCheck(t, err)
	call("GET", path+"/source", "", "ErrorEnvelope", 403)
	call("GET", download, "", "ErrorEnvelope", 403)
	call("GET", path+"/versions", "", "PDFVersionsEnvelope", 200)
	token = "invalid-session"
	call("GET", path+"/source", "", "ErrorEnvelope", 401)
}
