package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Schemas are fetched from the actual served document, not copied expectations.
func TestPairingOpenAPIConformanceHTTP(t *testing.T) {
	pool, client, web, sessions, _, admin := permissionHTTPFixture(t)
	api := apiv1.NewRouter(pool, client, sessions)
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	if document.Code != 200 {
		t.Fatal("OpenAPI unavailable")
	}
	var doc map[string]any
	if err := json.Unmarshal(document.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Fatal("not OpenAPI 3.1")
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://protocol.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	validate := func(fragment string, raw []byte) {
		t.Helper()
		schema, err := compiler.Compile("https://protocol.test/openapi.json#" + fragment)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err = schema.Validate(value); err != nil {
			t.Fatalf("schema %s: %v", fragment, err)
		}
	}
	resolve := func(fragment string) map[string]any {
		var value any = doc
		for _, key := range strings.Split(strings.TrimPrefix(fragment, "/"), "/") {
			key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
			value = value.(map[string]any)[key]
		}
		return value.(map[string]any)
	}
	checked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/sessions/") {
			path = "/sessions/{sessionID}"
		}
		paths := doc["paths"].(map[string]any)
		item, ok := paths[path].(map[string]any)
		if !ok {
			t.Fatalf("undocumented path %s", path)
		}
		operation := item[strings.ToLower(r.Method)].(map[string]any)
		operationFragment := "/paths/" + strings.ReplaceAll(path, "/", "~1") + "/" + strings.ToLower(r.Method)
		invalidRequest := false
		if _, ok := operation["requestBody"]; ok {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			schema, err := compiler.Compile("https://protocol.test/openapi.json#" + operationFragment + "/requestBody/content/application~1json/schema")
			if err != nil {
				t.Fatal(err)
			}
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			invalidRequest = err != nil || schema.Validate(value) != nil
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		if invalidRequest && response.Code != 400 {
			t.Fatalf("schema-invalid request: %s %s returned %d", r.Method, path, response.Code)
		}
		security, present := operation["security"]
		if !present {
			security = doc["security"]
		}
		if len(security.([]any)) > 0 && r.Header.Get("Authorization") == "" && response.Code != 401 {
			t.Fatalf("OpenAPI requires bearer: %s %s returned %d", r.Method, path, response.Code)
		}
		statuses := operation["responses"].(map[string]any)
		spec, ok := statuses[fmt.Sprint(response.Code)].(map[string]any)
		if !ok {
			t.Fatalf("undocumented %s %s status %d", r.Method, path, response.Code)
		}
		fragment := operationFragment + "/responses/" + fmt.Sprint(response.Code)
		for {
			ref, ok := spec["$ref"].(string)
			if !ok {
				break
			}
			if !strings.HasPrefix(ref, "#/") {
				t.Fatalf("unsupported response reference %s", ref)
			}
			fragment = strings.TrimPrefix(ref, "#")
			spec = resolve(fragment)
		}
		if headers, ok := spec["headers"].(map[string]any); ok {
			for name := range headers {
				value := response.Header().Get(name)
				if value == "" {
					if name == "X-Request-ID" || (name == "Retry-After" && (response.Code == 429 || response.Code == 503)) {
						t.Fatalf("missing %s", name)
					}
					continue
				}
				raw, _ := json.Marshal(value)
				validate(fragment+"/headers/"+name+"/schema", raw)
			}
		}
		if response.Code == 204 {
			if response.Body.Len() != 0 {
				t.Fatal("204 has body")
			}
		} else {
			mediaType, _, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
			if err != nil || mediaType != "application/json" {
				t.Fatalf("unexpected content type %q", response.Header().Get("Content-Type"))
			}
			validate(fragment+"/content/application~1json/schema", response.Body.Bytes())
		}
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	})
	expect := func(r *http.Request, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		checked.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: got %d want %d", r.Method, r.URL.Path, w.Code, status)
		}
		return w
	}
	for _, path := range []string{"/", "/me", "/sessions", "/capabilities"} {
		status := 401
		if path == "/" {
			status = 200
		}
		expect(httptest.NewRequest("GET", path, nil), status)
	}
	for _, body := range []string{`null`, `{}`, `{"device_name":1}`, `{"device_name":"phone","operation_id":"bad"}`, `{"device_name":"phone","operation_id":"` + uuid.NewString() + `","password":"forbidden"}`} {
		if w := pairingCall(checked, "/pairings", body); w.Code != 400 {
			t.Fatalf("invalid request accepted: %d", w.Code)
		}
	}
	startBody := `{"device_name":"Schema phone","operation_id":"` + uuid.NewString() + `"}`
	validate("/components/schemas/StartRequest", []byte(startBody))
	w := pairingCall(checked, "/pairings", startBody)
	if w.Code != 201 {
		t.Fatalf("start=%d", w.Code)
	}
	var started struct {
		Data struct {
			DeviceCode string `json:"device_code"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	cancel := `{"device_code":"` + started.Data.DeviceCode + `","operation_id":"` + uuid.NewString() + `"}`
	validate("/components/schemas/CancelRequest", []byte(cancel))
	if w := pairingCall(checked, "/pairings/cancel", cancel); w.Code != 204 {
		t.Fatal("cancel failed")
	}
	body := approvedPairing(t, checked, web, sessionCookie(t, context.Background(), sessions, admin))
	validate("/components/schemas/RedeemRequest", []byte(body))
	w = pairingCall(checked, "/pairings/redeem", body)
	if w.Code != 201 {
		t.Fatalf("redeem=%d, want 201", w.Code)
	}
	var token struct {
		Data struct {
			Token string `json:"token"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &token); err != nil {
		t.Fatal(err)
	}
	if token.Data.Token == "" {
		t.Fatal("missing bearer")
	}
	if w := pairingCall(checked, "/pairings/redeem", body); w.Code != 409 {
		t.Fatalf("replay=%d", w.Code)
	}
	if w := pairingCall(checked, "/session", `{"password":"not-accepted"}`); w.Code != 410 {
		t.Fatalf("password login=%d", w.Code)
	}
	for _, path := range []string{"/me", "/capabilities", "/sessions"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token.Data.Token)
		expect(r, 200)
	}
	call := func(method, path, bearer string, status int) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		return expect(r, status)
	}
	newBearer := func() string {
		body := approvedPairing(t, checked, web, sessionCookie(t, context.Background(), sessions, admin))
		w := pairingCall(checked, "/pairings/redeem", body)
		if w.Code != 201 {
			t.Fatalf("fresh bearer=%d", w.Code)
		}
		var out struct {
			Data struct {
				Token string `json:"token"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Data.Token
	}
	other, survivor := newBearer(), newBearer()
	listed := call("GET", "/sessions", other, 200)
	var listing struct {
		Data []struct {
			ID      int64 `json:"id"`
			Current bool  `json:"current"`
		}
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	var otherID int64
	for _, s := range listing.Data {
		if s.Current {
			otherID = s.ID
		}
	}
	if otherID == 0 {
		t.Fatal("no current session")
	}
	call("DELETE", "/sessions/9223372036854775807", token.Data.Token, 404)
	call("DELETE", fmt.Sprintf("/sessions/%d", otherID), token.Data.Token, 204)
	call("GET", "/me", other, 401)
	call("DELETE", "/session", token.Data.Token, 204)
	call("GET", "/me", token.Data.Token, 401)
	call("DELETE", "/sessions", survivor, 204)
	call("GET", "/me", survivor, 401)
	// Mutation sensitivity: the served schema must reject malformed/unknown input.
	schema, err := compiler.Compile("https://protocol.test/openapi.json#/components/schemas/StartRequest")
	if err != nil {
		t.Fatal(err)
	}
	if schema.Validate(map[string]any{"device_name": "phone", "operation_id": uuid.NewString(), "password": "x"}) == nil {
		t.Fatal("request schema permits secret/unknown field")
	}
}
