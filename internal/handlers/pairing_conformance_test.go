package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	checked := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
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
		statuses := operation["responses"].(map[string]any)
		spec, ok := statuses[fmt.Sprint(response.Code)].(map[string]any)
		if !ok {
			t.Fatalf("undocumented %s %s status %d", r.Method, path, response.Code)
		}
		if response.Code == 204 {
			if response.Body.Len() != 0 {
				t.Fatal("204 has body")
			}
		} else {
			fragment := "/paths/" + strings.ReplaceAll(path, "/", "~1") + "/" + strings.ToLower(r.Method) + "/responses/" + fmt.Sprint(response.Code) + "/content/application~1json/schema"
			if _, ref := spec["$ref"]; ref {
				fragment = "/components/schemas/ErrorEnvelope"
			}
			validate(fragment, response.Body.Bytes())
		}
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	})
	for _, path := range []string{"/", "/me", "/sessions", "/capabilities"} {
		checked.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", path, nil))
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
	pairingCall(checked, "/pairings/redeem", body)
	pairingCall(checked, "/session", `{"password":"not-accepted"}`)
	for _, path := range []string{"/me", "/capabilities", "/sessions"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token.Data.Token)
		checked.ServeHTTP(httptest.NewRecorder(), r)
	}
	for _, path := range []string{"/sessions/9223372036854775807", "/session", "/sessions"} {
		r := httptest.NewRequest("DELETE", path, nil)
		r.Header.Set("Authorization", "Bearer "+token.Data.Token)
		checked.ServeHTTP(httptest.NewRecorder(), r)
	}
	// Mutation sensitivity: the served schema must reject malformed/unknown input.
	schema, err := compiler.Compile("https://protocol.test/openapi.json#/components/schemas/StartRequest")
	if err != nil {
		t.Fatal(err)
	}
	if schema.Validate(map[string]any{"device_name": "phone", "operation_id": uuid.NewString(), "password": "x"}) == nil {
		t.Fatal("request schema permits secret/unknown field")
	}
}
