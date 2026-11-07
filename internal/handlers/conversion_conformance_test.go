package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Validate the served contract at the production HTTP seam, including error
// envelopes and sync/provenance projections, rather than a copied test schema.
func conversionContract(t *testing.T, api http.Handler) http.Handler {
	t.Helper()
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var spec map[string]any
	historyCheck(t, json.Unmarshal(document.Body.Bytes(), &spec))
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	const resource = "https://conversion-contract.test/openapi.json"
	historyCheck(t, compiler.AddResource(resource, spec))
	escape := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }
	validate := func(pointer string, raw []byte) {
		t.Helper()
		schema, err := compiler.Compile(resource + "#" + pointer)
		historyCheck(t, err)
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		historyCheck(t, err)
		if err := schema.Validate(value); err != nil {
			t.Errorf("%s: %v", pointer, err)
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !(strings.HasPrefix(r.URL.Path, "/estimates") || strings.HasPrefix(r.URL.Path, "/invoices") || strings.HasPrefix(r.URL.Path, "/commercial-operations/") || strings.HasPrefix(r.URL.Path, "/sync/")) {
			api.ServeHTTP(w, r)
			return
		}
		var operation map[string]any
		var pointer string
		for template, path := range spec["paths"].(map[string]any) {
			parts, actual := strings.Split(template, "/"), strings.Split(r.URL.Path, "/")
			if len(parts) != len(actual) {
				continue
			}
			match := true
			for i, part := range parts {
				match = match && (strings.HasPrefix(part, "{") || part == actual[i])
			}
			if match {
				item := path.(map[string]any)
				pointer = "/paths/" + escape(template) + "/" + strings.ToLower(r.Method)
				if ref, ok := item["$ref"].(string); ok {
					name := strings.TrimPrefix(ref, "#/components/pathItems/")
					item = spec["components"].(map[string]any)["pathItems"].(map[string]any)[name].(map[string]any)
					pointer = "/components/pathItems/" + escape(name) + "/" + strings.ToLower(r.Method)
				}
				operation, _ = item[strings.ToLower(r.Method)].(map[string]any)
				break
			}
		}
		if operation == nil {
			t.Errorf("undocumented operation %s %s", r.Method, r.URL.Path)
			api.ServeHTTP(w, r)
			return
		}
		var body []byte
		if r.Body != nil {
			var err error
			body, err = io.ReadAll(r.Body)
			historyCheck(t, err)
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		if request, ok := operation["requestBody"].(map[string]any); ok && response.Code < 400 {
			if _, ok := request["content"]; ok {
				validate(pointer+"/requestBody/content/application~1json/schema", body)
			}
		}
		responses := operation["responses"].(map[string]any)
		status := fmt.Sprint(response.Code)
		if _, ok := responses[status]; !ok {
			status = "default"
		}
		definition, ok := responses[status].(map[string]any)
		if !ok {
			t.Errorf("undocumented response %s %d", r.URL.Path, response.Code)
		} else {
			responsePointer := pointer + "/responses/" + status
			if ref, ok := definition["$ref"].(string); ok {
				responsePointer = strings.TrimPrefix(ref, "#")
			}
			validate(responsePointer+"/content/application~1json/schema", response.Body.Bytes())
		}
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	})
}

func TestConversionMalformedConformanceAPI(t *testing.T) {
	f := newBatchCommercialFixture(t, "estimate")
	f.api = conversionContract(t, f.api)
	source := f.create(t)
	path := fmt.Sprintf("/estimates/%d/convert", source.ID)
	for _, scenario := range []string{"null", "array", "unknown", "null_revision", "null_fields", "wrong_job_type", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			in := map[string]any{"operation_id": newOperationKey(), "job_id": f.job, "revision": source.Revision}
			var raw string
			switch scenario {
			case "null":
				raw = "null"
			case "array":
				raw = "[]"
			case "unknown":
				in["force"] = true
			case "null_revision":
				in["revision"] = nil
			case "null_fields":
				in["custom_fields"] = nil
			case "wrong_job_type":
				in["job_id"] = "wrong"
			case "duplicate":
				raw = fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"revision":%q}`, in["operation_id"], f.job, source.Revision, source.Revision)
			}
			if raw == "" {
				raw = matrixBody(t, in)
			}
			lifecycleCall(t, f.api, f.token, "POST", path, raw, 400)
			f.read(t, source.ID)
		})
	}
	// Missing review is terminal, and remains the original terminal result after
	// a later successful conversion hides its parent. Validation never rewrites it.
	missing := map[string]any{"operation_id": newOperationKey(), "job_id": f.job}
	first := lifecycleCall(t, f.api, f.token, "POST", path, matrixBody(t, missing), 428)
	lifecycleCall(t, f.api, f.token, "POST", path, matrixBody(t, map[string]any{"operation_id": newOperationKey(), "job_id": f.job, "revision": source.Revision}), 200)
	replay := lifecycleCall(t, f.api, f.token, "POST", path, matrixBody(t, missing), 428)
	assertContains(t, first, `"code":"review_required"`)
	assertContains(t, replay, `"code":"review_required"`)
}
