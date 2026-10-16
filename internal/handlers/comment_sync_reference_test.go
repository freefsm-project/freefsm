package handlers

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestCommentSyncEncryptedReferenceHTTP(t *testing.T) {
	if os.Getenv("FREEFSM_OFFLINE_KEY_TESTS") != "1" {
		t.Skip("set FREEFSM_OFFLINE_KEY_TESTS=1 with actual Secret Service")
	}
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Encrypted Comments").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Original Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(admin).SaveX(ctx)
	var document int64
	if err := pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,title) VALUES($1,$2,$3,'Original document') RETURNING id`, company, customer.ID, job.ID).Scan(&document); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Encrypted Comments")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	documentResponse := httptest.NewRecorder()
	api.ServeHTTP(documentResponse, httptest.NewRequest("GET", "/openapi.json", nil))
	var schemaDocument map[string]any
	if err := json.Unmarshal(documentResponse.Body.Bytes(), &schemaDocument); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://comments.test/openapi.json", schemaDocument); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"OperationBatch", "OperationBatchEnvelope", "JobSyncEnvelope", "CommentReceiptEnvelope", "ErrorEnvelope"} {
		schema, err := compiler.Compile("https://comments.test/openapi.json#/components/schemas/" + name)
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
			t.Errorf("%s conformance: %v", name, err)
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
		if r.URL.Path == "/__fixture/unlink" {
			if _, err := pool.Exec(ctx, `UPDATE estimates SET job_id=NULL WHERE id=$1`, document); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/scope-loss" {
			if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, job.ID); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.Header.Get("X-Comment-Interrupt") == "yes" && r.URL.Path == "/sync/operations" {
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
				t.Errorf("committed prefix: %d %s", response.Code, response.Body.String())
			}
			validate("OperationBatchEnvelope", response.Body.Bytes())
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				_ = connection.Close()
			}
			return
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		name := ""
		switch {
		case r.URL.Path == "/sync/operations":
			name = "OperationBatchEnvelope"
		case r.URL.Path == "/sync/jobs":
			name = "JobSyncEnvelope"
		case strings.HasPrefix(r.URL.Path, "/comment-operations/"):
			name = "CommentReceiptEnvelope"
		}
		if response.Code >= 400 {
			name = "ErrorEnvelope"
		}
		if name != "" {
			validate(name, response.Body.Bytes())
		}
		for k, v := range response.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	defer server.Close()
	input, _ := json.Marshal(map[string]any{"url": server.URL, "token": token})
	runCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/comment-sync-http.cjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("encrypted Comment reference: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
