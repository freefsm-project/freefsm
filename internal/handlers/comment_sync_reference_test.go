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
	"github.com/google/uuid"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestCommentSyncEncryptedReferenceHTTP(t *testing.T) {
	commentSyncReference(t, "comment-sync-http.cjs", "")
}

func TestCommentSyncReceiptFirstScopeLossHTTP(t *testing.T) {
	for _, mode := range []string{"assignment", "document", "assignment-failure", "document-failure"} {
		t.Run(mode, func(t *testing.T) { commentSyncReference(t, "comment-receipt-first-http.cjs", mode) })
	}
}

func TestCommentSyncOriginalEligibilityHTTP(t *testing.T) {
	for _, mode := range []string{"convert-revert", "archive-restore", "relink", "relink-back", "assignment-aba"} {
		t.Run(mode, func(t *testing.T) { commentSyncReference(t, "comment-eligibility-http.cjs", "eligibility-"+mode) })
	}
}

func commentSyncReference(t *testing.T, script, mode string) {
	t.Helper()
	if os.Getenv("FREEFSM_OFFLINE_KEY_TESTS") != "1" {
		t.Skip("set FREEFSM_OFFLINE_KEY_TESTS=1 with actual Secret Service")
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	userID, role := admin, int64(0)
	if mode != "" {
		auth := authorization.New(pool)
		var err error
		role, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Comment reconnect", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "estimate.view", Scope: authorization.Assigned}, {Permission: "comment.add", Scope: authorization.Assigned}, {Permission: "comment.remove", Scope: authorization.Own}})
		if err != nil {
			t.Fatal(err)
		}
		userID = client.User.Create().SetCompanyID(company).SetEmail("reconnect@test").SetName("Reconnect author").SetRole("tech").SetPasswordHash("hash").SaveX(ctx).ID
		if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, userID, role); err != nil {
			t.Fatal(err)
		}
	}
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Encrypted Comments").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Original Job").SaveX(ctx)
	client.JobAssignment.Create().SetJobID(job.ID).SetUserID(userID).SaveX(ctx)
	var document int64
	var otherJob int64
	var adminCookie *http.Cookie
	if strings.HasPrefix(mode, "eligibility-") {
		adminCookie = sessionCookie(t, ctx, sessions, admin)
		documentTransactionRequest(t, web, adminCookie, "POST", "/estimates", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Original document"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
		if err := pool.QueryRow(ctx, `SELECT id FROM estimates WHERE company_id=$1`, company).Scan(&document); err != nil {
			t.Fatal(err)
		}
		otherJob = client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Other authorized Job").SaveX(ctx).ID
		client.JobAssignment.Create().SetJobID(otherJob).SetUserID(userID).SaveX(ctx)
	} else {
		if err := pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,job_id,title) VALUES($1,$2,$3,'Original document') RETURNING id`, company, customer.ID, job.ID).Scan(&document); err != nil {
			t.Fatal(err)
		}
	}
	token, err := sessions.CreateMobile(ctx, userID, "Encrypted Comments")
	if err != nil {
		t.Fatal(err)
	}
	// This multi-action conformance journey is not a default-rate-budget test.
	// Use the supported configured budget, as the accepted field-sync harness does.
	api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100})
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
		if r.URL.Path == "/__fixture/comment-counts" {
			var comments, audits int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM comments),(SELECT count(*) FROM authorization_audit WHERE action IN('comment.add','comment.remove'))`).Scan(&comments, &audits); err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"comments": comments, "audits": audits})
			return
		}
		if r.URL.Path == "/__fixture/unrelated-edit" || r.URL.Path == "/__fixture/eligibility-change" {
			base := fmt.Sprintf("/estimates/%d", document)
			if r.URL.Path == "/__fixture/unrelated-edit" {
				client.Job.UpdateOneID(job.ID).SetJobType("Unrelated title edit").SaveX(ctx)
				values := commercialReviewedValues(t, web, adminCookie, base, url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Unrelated document edit"}, "line_items": {"[]"}, "tax_rate": {"0"}})
				documentTransactionRequest(t, web, adminCookie, "POST", base, values, 303)
			} else {
				switch mode {
				case "eligibility-convert-revert":
					commentPost(t, web, adminCookie, base+"/convert", url.Values{"idempotency_key": {uuid.NewString()}}, 303)
					var invoice int64
					if err := pool.QueryRow(ctx, `SELECT invoice_id FROM estimate_invoice_conversion_cycles WHERE estimate_id=$1 AND reverted_at IS NULL`, document).Scan(&invoice); err != nil {
						t.Fatal(err)
					}
					commentPost(t, web, adminCookie, fmt.Sprintf("/invoices/%d/revert-to-estimate", invoice), url.Values{"idempotency_key": {uuid.NewString()}}, 303)
				case "eligibility-archive-restore":
					commentPost(t, web, adminCookie, base+"/delete", nil, 303)
					commentPost(t, web, adminCookie, base+"/restore", nil, 303)
				case "eligibility-relink", "eligibility-relink-back":
					values := commercialReviewedValues(t, web, adminCookie, base, url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "job_id": {fmt.Sprint(otherJob)}, "title": {"Relinked document"}, "line_items": {"[]"}, "tax_rate": {"0"}})
					documentTransactionRequest(t, web, adminCookie, "POST", base, values, 303)
					if mode == "eligibility-relink-back" {
						back := commercialReviewedValues(t, web, adminCookie, base, url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "job_id": {fmt.Sprint(job.ID)}, "title": {"Returned original document"}, "line_items": {"[]"}, "tax_rate": {"0"}})
						documentTransactionRequest(t, web, adminCookie, "POST", base, back, 303)
					}
				case "eligibility-assignment-aba":
					if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, userID); err != nil {
						t.Fatal(err)
					}
					if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, job.ID, userID); err != nil {
						t.Fatal(err)
					}
				default:
					t.Error("unknown eligibility fixture")
				}
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/document-loss" {
			if _, err := pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='estimate.view'`, role); err != nil {
				t.Error(err)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/restore-access" {
			if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, job.ID, userID); err != nil {
				t.Error(err)
			}
			if _, err := pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,'estimate.view','J') ON CONFLICT DO NOTHING`, role); err != nil {
				t.Error(err)
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
	input, _ := json.Marshal(map[string]any{"url": server.URL, "token": token, "mode": mode})
	runCtx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "node", "../api/v1/testdata/"+script)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	t.Logf("encrypted Comment reference: %s", out)
	if err != nil {
		t.Fatal(err)
	}
}
