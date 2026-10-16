package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestJobFieldsProtectedReferenceHTTP(t *testing.T) {
	if os.Getenv("FREEFSM_OFFLINE_KEY_TESTS") != "1" {
		t.Skip("set FREEFSM_OFFLINE_KEY_TESTS=1 with actual Secret Service")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Protected field customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Protected field job").SaveX(ctx)
	worker := client.User.Create().SetCompanyID(company).SetEmail("protected-fields@test").SetName("Field worker").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, worker.ID, 3); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(worker.ID).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	for _, typ := range []string{"text", "textarea", "number", "date", "checkbox", "select", "multiselect"} {
		form := url.Values{"name": {typ}, "object_type": {"job"}, "field_type": {typ}, "precision": {"2"}, "options": {`["A","B"]`}}
		if typ == "checkbox" {
			form.Set("required", "true")
		}
		fieldPost(t, web, cookie, "/settings/custom-fields", form, 303)
	}
	token, err := sessions.CreateMobile(ctx, worker.ID, "Protected fields")
	if err != nil {
		t.Fatal(err)
	}
	// This long interactive journey uses the supported configurable sync budget;
	// dedicated transport tests retain the default-budget throttle assertions.
	api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100})
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var schemaDocument map[string]any
	if err := json.Unmarshal(document.Body.Bytes(), &schemaDocument); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://fields.test/openapi.json", schemaDocument); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"OperationBatch", "OperationBatchEnvelope", "JobSyncEnvelope", "JobExecutionReceiptEnvelope", "ErrorEnvelope"} {
		schema, err := compiler.Compile("https://fields.test/openapi.json#/components/schemas/" + name)
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
		if r.URL.Path == "/__fixture/fresh-token" {
			fresh, err := sessions.CreateMobile(ctx, worker.ID, "Fresh field reconciliation")
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
			return
		}
		if r.URL.Path == "/__fixture/options" {
			for id, typ := range map[int64]string{6: "select", 7: "multiselect"} {
				_, err := authorization.New(pool).SaveField(ctx, authorization.Actor{UserID: admin}, id, authorization.FieldInput{Name: typ, Type: typ, Options: `["A","B"]`, RetiredOptions: `["B"]`})
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/required" {
			value := "never backfilled"
			_, err := authorization.New(pool).SaveField(ctx, authorization.Actor{UserID: admin}, 0, authorization.FieldInput{ObjectType: "job", Name: "Added required", Type: "text", Required: true, Default: &value})
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/retire" {
			_, err := pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=true WHERE object_type='job' AND field_type='text'`)
			if err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/reactivate" {
			_, err := pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=false WHERE object_type='job' AND field_type='text'`)
			if err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/remove" {
			_, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID)
			if err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		name := ""
		switch {
		case r.URL.Path == "/sync/operations":
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			validate("OperationBatch", raw)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			name = "OperationBatchEnvelope"
		case r.URL.Path == "/sync/jobs":
			name = "JobSyncEnvelope"
		case strings.HasPrefix(r.URL.Path, "/job-field-operations/"):
			name = "JobExecutionReceiptEnvelope"
		}
		if name == "" {
			api.ServeHTTP(w, r)
			return
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		if response.Code >= 400 {
			name = "ErrorEnvelope"
		}
		validate(name, response.Body.Bytes())
		for k, v := range response.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	defer server.Close()
	input, _ := json.Marshal(map[string]any{"url": server.URL, "token": token, "job_id": j.ID})
	runCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/job-fields-protected-http.cjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("protected fields: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
