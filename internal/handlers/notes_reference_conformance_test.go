package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Run the executable client against actual routes, validating every exchange
// against the locally served OpenAPI 3.1 document (JSON Schema 2020-12).
func TestNotesReferenceOpenAPIHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reference owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reference Notes").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Notes conformance actor", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.field_notes.edit", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	actor := client.User.Create().SetCompanyID(company).SetName("Notes writer").SetEmail("notes-conformance@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, actor.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, actor.ID); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, actor.ID, "Notes conformance")
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
	if doc["openapi"] != "3.1.0" {
		t.Fatal("not OpenAPI 3.1")
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://notes.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	// Compile before starting the concurrent HTTP server.
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"JobNotesCommand", "JobNotesEnvelope", "JobNotesHistoryEnvelope", "JobExecutionReceiptEnvelope", "ErrorEnvelope"} {
		schema, err := compiler.Compile("https://notes.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		schemas[name] = schema
	}
	validate := func(name string, raw []byte) error {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		return schemas[name].Validate(value)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := "/jobs/{id}/field-notes"
		name := "JobNotesEnvelope"
		if strings.HasSuffix(r.URL.Path, "/history") {
			path += "/history"
			name = "JobNotesHistoryEnvelope"
		}
		if strings.HasPrefix(r.URL.Path, "/job-execution-operations/") {
			path = "/job-execution-operations/{operation}"
			name = "JobExecutionReceiptEnvelope"
		}
		if r.Method == "POST" {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			if err := validate("JobNotesCommand", raw); err != nil {
				t.Errorf("request schema: %v", err)
			}
			name = "JobExecutionReceiptEnvelope"
		}
		response := httptest.NewRecorder()
		// Harness-only dependency failure exercises the same outer-admission
		// error writer used by production maintenance/recovery middleware.
		if r.Header.Get("X-Notes-Fault") == "unavailable" {
			apiv1.WriteUnavailable(response)
		} else {
			api.ServeHTTP(response, r)
		}
		operation := doc["paths"].(map[string]any)[path].(map[string]any)[strings.ToLower(r.Method)].(map[string]any)
		if _, ok := operation["responses"].(map[string]any)[fmt.Sprint(response.Code)]; !ok {
			t.Errorf("undocumented status %s %s: %d", r.Method, path, response.Code)
		}
		if response.Code >= 400 {
			name = "ErrorEnvelope"
		}
		if err := validate(name, response.Body.Bytes()); err != nil {
			t.Errorf("response schema %s: %v", name, err)
		}
		if r.Header.Get("X-Notes-Fault") == "lose-and-unassign" && response.Code == 200 {
			if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, j.ID, actor.ID); err != nil {
				t.Error(err)
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		for key, values := range response.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(response.Code)
		_, _ = w.Write(response.Body.Bytes())
	}))
	defer server.Close()
	runCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(runCtx, "node", "testdata/browser/notes-receipt-reference.cjs", server.URL, fmt.Sprint(j.ID), token).CombinedOutput()
	t.Logf("reference conformance: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
