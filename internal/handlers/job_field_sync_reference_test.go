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
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestJobFieldSyncEncryptedReferenceHTTP(t *testing.T) {
	if os.Getenv("FREEFSM_OFFLINE_KEY_TESTS") != "1" {
		t.Skip("set FREEFSM_OFFLINE_KEY_TESTS=1 with actual Secret Service")
	}
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Encrypted field sync").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Encrypted field actions").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(j.ID).SetUserID(admin).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := fmt.Sprintf("/jobs/%d/subtasks", j.ID)
	masterPost(t, router, cookie, path, url.Values{"action": {"add"}, "title": {"Original task"}, "operation_id": {uuid.NewString()}}, 303)
	flow := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	target, err := flow.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Job, Name: "Reviewed target", Color: "#123456", Category: statusflow.JobPending})
	if err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Encrypted fields")
	if err != nil {
		t.Fatal(err)
	}
	// This long conformance journey exceeds the default twelve sync requests;
	// use the documented configurable budget without bypassing its middleware.
	api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100})
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc map[string]any
	if err := json.Unmarshal(document.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://fields.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"OperationBatch", "OperationBatchEnvelope", "JobSyncEnvelope", "ErrorEnvelope"} {
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
			t.Errorf("%s: %v", name, err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/__fixture/") {
			var err error
			switch strings.TrimPrefix(r.URL.Path, "/__fixture/") {
			case "restore":
				_, err = pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`)
			case "token":
				fresh, tokenErr := sessions.CreateMobile(ctx, admin, "Restored field reference")
				if tokenErr != nil {
					t.Error(tokenErr)
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
				return
			case "rename":
				_, err = flow.Update(ctx, actor, target.ID, "Cosmetic target", "#654321")
			case "category":
				err = flow.Move(ctx, actor, statusflow.MoveRequest{StatusID: target.ID, Category: statusflow.JobCompleted, Order: 1, ConfirmInUse: true})
			case "replace":
				var replacement statusflow.Status
				replacement, err = flow.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Job, Name: "Replacement target", Color: "#654321", Category: statusflow.JobCompleted})
				if err == nil {
					err = flow.Delete(ctx, actor, target.ID, replacement.ID)
				}
				if err == nil {
					_, err = flow.Update(ctx, actor, replacement.ID, "Cosmetic target", "#654321")
				}
			case "remove":
				var id string
				err = pool.QueryRow(ctx, `SELECT id::text FROM job_subtasks WHERE job_id=$1 AND removed_at IS NULL`, j.ID).Scan(&id)
				if err == nil {
					masterPost(t, router, sessionCookie(t, ctx, sessions, admin), path, url.Values{"action": {"remove"}, "subtask_id": {id}, "operation_id": {uuid.NewString()}}, 303)
				}
			default:
				t.Error("unknown field fixture")
			}
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/sync/operations" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			validate("OperationBatch", raw)
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		if response.Code >= 400 {
			validate("ErrorEnvelope", response.Body.Bytes())
		} else if r.URL.Path == "/sync/jobs" {
			validate("JobSyncEnvelope", response.Body.Bytes())
		} else if r.URL.Path == "/sync/operations" {
			validate("OperationBatchEnvelope", response.Body.Bytes())
		}
		for k, v := range response.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	defer server.Close()
	input, _ := json.Marshal(map[string]any{"url": server.URL, "token": token, "target": fmt.Sprint(target.ID)})
	runCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/job-field-sync-http.cjs")
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("encrypted field conformance: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "PASS encrypted Status/Subtask") {
		t.Fatal("reference assertions did not finish")
	}
}
