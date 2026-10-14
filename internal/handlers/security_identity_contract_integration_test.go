package handlers

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestSecurityIdentityRequestAndSchemaHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	token, err := sessions.CreateMobile(context.Background(), admin, "schema device")
	if err != nil {
		t.Fatal(err)
	}
	router := apiv1.NewRouter(pool, client, sessions)
	for _, body := range []string{`null`, `[]`, `{"unknown":true}`} {
		if w := securityRequest(router, token, "POST", "/time-entries/lease", uuid.NewString(), body); w.Code != 400 {
			t.Fatalf("invalid lease body %s = %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/time-entries/lease", "/session", "/sessions", "/sessions/1"} {
		method, body := "DELETE", ""
		if path == "/time-entries/lease" {
			method, body = "POST", `{}`
		}
		for _, id := range []string{"", "invalid", uuid.Nil.String()} {
			if w := securityRequest(router, token, method, path, id, body); w.Code != 400 {
				t.Fatalf("missing/malformed identity %s = %d", path, w.Code)
			}
		}
	}
	document := httptest.NewRecorder()
	router.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc map[string]any
	if err := json.Unmarshal(document.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://security.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	validate := func(schemaName string, w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != 200 {
			t.Fatalf("schema response=%d %s", w.Code, w.Body.String())
		}
		schema, err := compiler.Compile("https://security.test/openapi.json#/components/schemas/" + schemaName)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(strings.NewReader(w.Body.String()))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatal(err)
		}
	}
	leaseID := uuid.NewString()
	validate("ClockLeaseReceiptEnvelope", securityRequest(router, token, "POST", "/time-entries/lease", leaseID, `{}`))
	validate("ClockLeaseReceiptEnvelope", securityRequest(router, token, "GET", "/clock-lease-operations/"+leaseID, "", ""))
	sessionID := uuid.NewString()
	if w := securityRequest(router, token, "DELETE", "/sessions/9223372036854775807", sessionID, ""); w.Code != 404 {
		t.Fatalf("unknown target=%d", w.Code)
	}
	validate("SessionRevocationReceiptEnvelope", securityRequest(router, token, "GET", "/session-operations/"+sessionID, "", ""))
	// A terminal missing target has no sensitive identity and still reserves its UUID.
	if w := securityRequest(router, token, "POST", "/time-entries/lease", sessionID, `{}`); w.Code != 409 {
		t.Fatalf("session-first collision=%d %s", w.Code, w.Body.String())
	}
}

// Migrated from api/v1's fake-only endpoint test. Retain its response/security
// assertions while observing actual bearer invalidation, not a fake method call.
func TestLogoutRevokesBearerSession(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "logout contract")
	if err != nil {
		t.Fatal(err)
	}
	router := apiv1.NewRouter(pool, client, sessions)
	id := uuid.NewString()
	res := securityRequest(router, token, "DELETE", "/session", id, "")
	if res.Code != 204 || res.Body.Len() != 0 {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %q", res.Header().Get("Cache-Control"))
	}
	if _, err := sessions.ValidateMobile(ctx, token); err == nil {
		t.Fatal("logout bearer remains valid")
	}
	if got := securityRequest(router, token, "GET", "/me", "", ""); got.Code != 401 {
		t.Fatalf("revoked bearer HTTP=%d", got.Code)
	}
	fresh, err := sessions.CreateMobile(ctx, admin, "fresh logout reconciliation")
	if err != nil {
		t.Fatal(err)
	}
	data := securityData(t, securityRequest(router, fresh, "GET", "/session-operations/"+id, "", ""), 200)
	if !strings.Contains(string(data), `"code":"accepted"`) {
		t.Fatalf("logout receipt=%s", data)
	}
}
