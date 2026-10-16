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
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestOperationBatchProtectedReferenceHTTP(t *testing.T) {
	if os.Getenv("FREEFSM_OFFLINE_KEY_TESTS") != "1" {
		t.Skip("set FREEFSM_OFFLINE_KEY_TESTS=1 with actual Secret Service")
	}
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Batch reference").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Batch reference original").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(admin).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Batch reference")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc map[string]any
	if err := json.Unmarshal(document.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://batch.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"OperationBatch", "OperationBatchEnvelope", "ErrorEnvelope"} {
		schema, err := compiler.Compile("https://batch.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		schemas[name] = schema
	}
	validate := func(name string, raw []byte) {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Error(err)
			return
		}
		if err := schemas[name].Validate(value); err != nil {
			t.Errorf("%s schema: %v", name, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/operations" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			validate("OperationBatch", raw)
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		if r.URL.Path == "/__fixture/restore" {
			if _, err := pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/token" {
			fresh, err := sessions.CreateMobile(ctx, admin, "Restored batch")
			if err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
			return
		}
		if r.Header.Get("X-Batch-Interrupt") == "yes" && r.URL.Path == "/sync/operations" {
			// Simulate interruption after the first operation's real transaction,
			// before the rest is dispatched, then lose the actual HTTP response.
			var body map[string][]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				return
			}
			body["operations"] = body["operations"][:1]
			raw, _ := json.Marshal(body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			response := httptest.NewRecorder()
			api.ServeHTTP(response, r)
			if response.Code != 200 {
				t.Errorf("partial commit %d %s", response.Code, response.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				_ = conn.Close()
			}
			return
		}
		if r.URL.Path == "/sync/operations" {
			response := httptest.NewRecorder()
			api.ServeHTTP(response, r)
			name := "OperationBatchEnvelope"
			if response.Code >= 400 {
				name = "ErrorEnvelope"
			}
			validate(name, response.Body.Bytes())
			operation := doc["paths"].(map[string]any)["/sync/operations"].(map[string]any)["post"].(map[string]any)
			if _, ok := operation["responses"].(map[string]any)[fmt.Sprint(response.Code)]; !ok {
				t.Errorf("undocumented batch status%d", response.Code)
			}
			for k, v := range response.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(response.Code)
			_, _ = w.Write(response.Body.Bytes())
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	input, _ := json.Marshal(map[string]any{"url": server.URL, "token": token})
	runCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/operation-batches-http.cjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("encrypted batches: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "PASS encrypted batch restart") {
		t.Fatal("reference assertions did not finish")
	}
}
