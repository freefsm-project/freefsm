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
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestOfflineNotesProtectedReferenceHTTP(t *testing.T) {
	if os.Getenv("FREEFSM_OFFLINE_KEY_TESTS") != "1" {
		t.Skip("set FREEFSM_OFFLINE_KEY_TESTS=1 with a real unlocked Secret Service")
	}
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Offline owner").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Offline original target").SaveX(ctx)
	hiddenJob := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Restore lost target").SaveX(ctx)
	user := client.User.Create().SetCompanyID(company).SetName("Offline field user").SetEmail("offline79@test").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Offline Notes actor", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "job.field_notes.edit", Scope: authorization.Assigned}})
	if err != nil {
		t.Fatal(err)
	}
	if err = auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role); err != nil {
		t.Fatal(err)
	}
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(user.ID).SaveX(ctx)
	client.JobAssignment.Create().SetJobID(hiddenJob.ID).SetUserID(user.ID).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, user.ID, "Offline reference")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	document := httptest.NewRecorder()
	api.ServeHTTP(document, httptest.NewRequest("GET", "/openapi.json", nil))
	var doc map[string]any
	if err = json.Unmarshal(document.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err = compiler.AddResource("https://offline.test/openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"OfflinePolicyEnvelope", "OfflineAuthorizationEnvelope", "JobSyncEnvelope", "JobNotesEnvelope", "JobNotesCommand", "JobExecutionReceiptEnvelope", "ErrorEnvelope"} {
		schema, err := compiler.Compile("https://offline.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		schemas[name] = schema
	}
	schemas["LeaseRequest"], err = compiler.Compile("https://offline.test/openapi.json#/paths/~1offline~1lease/post/requestBody/content/application~1json/schema")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Fixture controls model real committed changes; no production bypass route.
		if strings.HasPrefix(r.URL.Path, "/__fixture/") {
			var err error
			switch strings.TrimPrefix(r.URL.Path, "/__fixture/") {
			case "unassign":
				_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, user.ID)
			case "unassign-hidden":
				_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, hiddenJob.ID, user.ID)
			case "assign":
				_, err = pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, job.ID, user.ID)
			case "restore":
				_, err = pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`)
			case "token":
				var fresh string
				fresh, err = sessions.CreateMobile(ctx, user.ID, "Fresh restored reference")
				if err == nil {
					_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
					return
				}
			case "disable":
				_, err = pool.Exec(ctx, `UPDATE company_settings SET offline_hours=0 WHERE company_id=$1`, company)
			case "enable":
				_, err = pool.Exec(ctx, `UPDATE company_settings SET offline_hours=24 WHERE company_id=$1`, company)
			case "remove-job-view":
				_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission IN ('job.view','job.field_notes.edit')`, role)
			case "restore-job-view":
				_, err = pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'job.view',$2),($1,'job.field_notes.edit',$2)`, role, authorization.Assigned)
			case "revoke-all":
				_, err = pool.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE user_id=$1`, user.ID)
			case "revoke":
				err = sessions.RevokeMobile(ctx, token)
			default:
				http.NotFound(w, r)
				return
			}
			if err != nil {
				t.Errorf("fixture: %v", err)
				w.WriteHeader(500)
			} else {
				w.WriteHeader(204)
			}
			return
		}
		if r.Method == "POST" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			name := "JobNotesCommand"
			if r.URL.Path == "/offline/lease" {
				name = "LeaseRequest"
			}
			value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				t.Error(err)
			} else if err = schemas[name].Validate(value); err != nil {
				t.Errorf("request schema %s: %v", name, err)
			}
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, r)
		name := "JobNotesEnvelope"
		switch {
		case strings.HasPrefix(r.URL.Path, "/offline/policy"):
			name = "OfflinePolicyEnvelope"
		case strings.HasPrefix(r.URL.Path, "/offline/lease"), strings.HasPrefix(r.URL.Path, "/offline-authorization-operations/"):
			name = "OfflineAuthorizationEnvelope"
		case strings.HasPrefix(r.URL.Path, "/sync/jobs"):
			name = "JobSyncEnvelope"
		case strings.HasPrefix(r.URL.Path, "/job-execution-operations/"), r.Method == "POST":
			name = "JobExecutionReceiptEnvelope"
		}
		if response.Code >= 400 {
			name = "ErrorEnvelope"
		}
		contractPath := r.URL.Path
		if strings.HasPrefix(contractPath, "/jobs/") {
			contractPath = "/jobs/{id}/field-notes"
		}
		if strings.HasPrefix(contractPath, "/job-execution-operations/") {
			contractPath = "/job-execution-operations/{operation}"
		}
		if strings.HasPrefix(contractPath, "/offline-authorization-operations/") {
			contractPath = "/offline-authorization-operations/{operation}"
		}
		operation := doc["paths"].(map[string]any)[contractPath].(map[string]any)[strings.ToLower(r.Method)].(map[string]any)
		if _, ok := operation["responses"].(map[string]any)[fmt.Sprint(response.Code)]; !ok {
			t.Errorf("undocumented status %s %d", contractPath, response.Code)
		}
		value, decodeErr := jsonschema.UnmarshalJSON(bytes.NewReader(response.Body.Bytes()))
		if decodeErr != nil {
			t.Errorf("decode %s %d: %v", r.URL.Path, response.Code, decodeErr)
		} else if err := schemas[name].Validate(value); err != nil {
			t.Errorf("schema %s %s: %v", r.URL.Path, name, err)
		}
		if r.Header.Get("X-Offline-Lose-Response") == "yes" && (response.Code == 200 || (r.URL.Path == "/offline/lease" && response.Code == 403)) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
			} else {
				_ = conn.Close()
			}
			return
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
	command := exec.CommandContext(runCtx, "node", "../api/v1/testdata/offline-notes-http.cjs")
	command.Stdin = bytes.NewReader(input)
	out, err := command.CombinedOutput()
	t.Logf("protected reference: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	var notes string
	if err = pool.QueryRow(ctx, `SELECT tech_notes FROM jobs WHERE id=$1`, job.ID).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if notes != "accepted original" {
		t.Fatalf("unexpected replay/change: %q", notes)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='job.field_notes.edit' AND target_id=$1`, job.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("duplicate Notes effect: %d", count)
	}
}
