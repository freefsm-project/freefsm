package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestJobFieldsMixedExecutionProtectedReferenceHTTP(t *testing.T) {
	if os.Getenv("FREEFSM_OFFLINE_KEY_TESTS") != "1" {
		t.Skip("set FREEFSM_OFFLINE_KEY_TESTS=1 with actual Secret Service")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Mixed field customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Mixed independent field actions").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetEmail("mixed-fields@test").SetName("Mixed field worker").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := authorization.New(pool).AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, 3); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(u.ID).SaveX(ctx)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Reading"}, "object_type": {"job"}, "field_type": {"text"}}, 303)
	masterPost(t, web, cookie, fmt.Sprintf("/jobs/%d/subtasks", j.ID), url.Values{"action": {"add"}, "title": {"Mixed task"}, "operation_id": {newOperationKey()}}, 303)
	token, err := sessions.CreateMobile(ctx, u.ID, "Mixed field reference")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100})
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc map[string]any
	if err := json.Unmarshal(document.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://mixed.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"OperationBatch", "OperationBatchEnvelope", "JobSyncEnvelope", "ClockSyncEnvelope", "ClockReceiptEnvelope", "JobExecutionReceiptEnvelope", "SubtaskReceiptEnvelope", "ErrorEnvelope"} {
		schema, err := compiler.Compile("https://mixed.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		schemas[name] = schema
	}
	validate := func(name string, raw []byte, valid bool) {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			t.Error(err)
			return
		}
		err = schemas[name].Validate(value)
		if (err == nil) != valid {
			t.Errorf("%s expected valid=%t: %v", name, valid, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fixture/remove" {
			if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1`, j.ID); err != nil {
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
			validate("OperationBatch", raw, r.Header.Get("X-Mixed-Malformed") != "yes")
			r.Body = io.NopCloser(bytes.NewReader(raw))
			name = "OperationBatchEnvelope"
		case r.URL.Path == "/sync/jobs":
			name = "JobSyncEnvelope"
		case r.URL.Path == "/sync/clocks":
			name = "ClockSyncEnvelope"
		case strings.HasPrefix(r.URL.Path, "/time-operations/"):
			name = "ClockReceiptEnvelope"
		case strings.HasPrefix(r.URL.Path, "/job-field-operations/") || strings.HasPrefix(r.URL.Path, "/job-execution-operations/"):
			name = "JobExecutionReceiptEnvelope"
		case strings.HasPrefix(r.URL.Path, "/subtask-operations/"):
			name = "SubtaskReceiptEnvelope"
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
		validate(name, response.Body.Bytes(), true)
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
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/job-fields-mixed-http.cjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("mixed protected field conformance: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
