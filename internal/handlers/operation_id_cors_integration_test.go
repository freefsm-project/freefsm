package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/google/uuid"
)

func TestOperationIDConfiguredOriginPreflightHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	const origin = "https://field.example"
	api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{CORSOrigins: []string{origin}})
	server := httptest.NewTLSServer(api)
	defer server.Close()
	call := func(method, path, body string, headers map[string]string) (int, http.Header, string) {
		t.Helper()
		r, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for key, value := range headers {
			r.Header.Set(key, value)
		}
		response, err := server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, response.Header, string(raw)
	}
	for _, target := range []struct{ method, path string }{
		{"POST", "/time-entries/lease"},
		{"DELETE", "/session"},
		{"PUT", "/file-transfers/" + uuid.NewString() + "/chunks?offset=0"},
	} {
		for _, requested := range []string{"authorization, operation-id", "Authorization, Operation-ID", " AUTHORIZATION , oPeRaTiOn-Id , Content-Type "} {
			t.Run(target.method+"/"+requested, func(t *testing.T) {
				status, headers, body := call("OPTIONS", target.path, "", map[string]string{
					"Origin": origin, "Access-Control-Request-Method": target.method, "Access-Control-Request-Headers": requested,
				})
				if status != http.StatusNoContent || body != "" || headers.Get("Access-Control-Allow-Origin") != origin {
					t.Fatalf("configured-origin preflight: status=%d headers=%v body=%s", status, headers, body)
				}
				if !strings.Contains(strings.ToLower(headers.Get("Access-Control-Allow-Headers")), "operation-id") || !strings.Contains(headers.Get("Access-Control-Allow-Methods"), target.method) || headers.Get("Access-Control-Allow-Credentials") != "" {
					t.Fatalf("incorrect bearer preflight grant: %v", headers)
				}
			})
		}
	}
	for _, denied := range []struct{ origin, requested string }{
		{"http://field.example", "authorization, operation-id"},
		{"https://field.example.evil", "authorization, operation-id"},
		{"https://field.example:444", "authorization, operation-id"},
		{"null", "authorization, operation-id"},
		{origin, "authorization, operation-id, X-Unapproved"},
	} {
		t.Run("denied/"+denied.origin+"/"+denied.requested, func(t *testing.T) {
			status, headers, body := call("OPTIONS", "/time-entries/lease", "", map[string]string{
				"Origin": denied.origin, "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": denied.requested,
			})
			if status != http.StatusForbidden || !strings.Contains(body, `"code":"origin_denied"`) || headers.Get("Access-Control-Allow-Methods") != "" {
				t.Fatalf("invalid preflight admitted: status=%d headers=%v body=%s", status, headers, body)
			}
			if denied.origin != origin && headers.Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("inexact origin received CORS grant")
			}
		})
	}
	// A successful preflight grants transport access, never product authority.
	headers := map[string]string{"Origin": origin, "Content-Type": "application/json", "oPeRaTiOn-Id": uuid.NewString()}
	if status, _, body := call("POST", "/time-entries/lease", "{}", headers); status != http.StatusUnauthorized {
		t.Fatalf("preflight bypassed authentication: %d %s", status, body)
	}
	token, err := sessions.CreateMobile(context.Background(), admin, "Configured-origin receipt client")
	if err != nil {
		t.Fatal(err)
	}
	headers["Authorization"] = "Bearer " + token
	status, responseHeaders, accepted := call("POST", "/time-entries/lease", "{}", headers)
	if status != http.StatusOK || responseHeaders.Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("receipt-backed request failed: %d %s", status, accepted)
	}
	var receipt struct {
		Data struct {
			OperationID string `json:"operation_id"`
			Code        string `json:"code"`
			ID          string `json:"id"`
		}
	}
	if err := json.Unmarshal([]byte(accepted), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Data.OperationID != headers["oPeRaTiOn-Id"] || receipt.Data.Code != "accepted" || receipt.Data.ID == "" {
		t.Fatalf("missing bound lease effect: %s", accepted)
	}
	if status, _, replay := call("POST", "/time-entries/lease", "{}", headers); status != http.StatusOK || replay != accepted {
		t.Fatalf("configured-origin replay changed receipt: %d %s", status, replay)
	}
	if status, _, reconciled := call("GET", "/clock-lease-operations/"+receipt.Data.OperationID, "", headers); status != http.StatusOK || reconciled != accepted {
		t.Fatalf("configured-origin reconciliation changed receipt: %d %s", status, reconciled)
	}
}
