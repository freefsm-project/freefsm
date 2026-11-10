package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
)

func TestPDFProtectedReferenceHTTP(t *testing.T) {
	for _, scenario := range []string{"lifecycle", "retry", "drift", "checkpoint", "acceptance_checkpoint", "restore", "revoke", "exact", "expiry", "conversion", "reversion", "download_only", "scope_loss", "scope_receipt", "archived", "read_loss_versions", "read_loss_download", "read_loss_versions_storage", "read_loss_download_storage", "read_loss_versions_purge_storage", "read_loss_download_purge_storage", "read_ambiguous_versions", "read_ambiguous_download", "read_malformed_versions", "read_malformed_download", "read_transport_versions", "read_transport_download", "checkpoint_write", "checkpoint_rename", "restore_repeated", "read_loss_versions_write", "read_loss_versions_rename", "read_loss_versions_purge_write", "read_loss_versions_purge_rename", "read_loss_download_write", "read_loss_download_rename", "read_loss_download_purge_write", "read_loss_download_purge_rename"} {
		t.Run(scenario, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Protected PDF Customer").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Protected PDF Job").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, j.ID, admin)
			kind := "invoice"
			if scenario == "conversion" || scenario == "reversion" {
				kind = "estimate"
			}
			var document int64
			if kind == "invoice" {
				document = client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("ENCRYPTED-PDF-REVIEW").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx).ID
			} else {
				document = client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("ENCRYPTED-PDF-REVIEW").SaveX(ctx).ID
			}
			user := admin
			var role int64
			if scenario == "download_only" || scenario == "scope_loss" || scenario == "scope_receipt" || strings.HasPrefix(scenario, "read_loss_") {
				auth := authorization.New(pool)
				var err error
				role, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "PDF reference grants", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "invoice.pdf.generate", Scope: authorization.Assigned}, {Permission: "invoice.pdf.download", Scope: authorization.Assigned}})
				historyCheck(t, err)
				user = client.User.Create().SetCompanyID(company).SetName("PDF user").SetEmail("pdf-protected@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx).ID
				historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user, role))
				assignFieldDocumentCreator(t, pool, j.ID, user)
			}
			token, err := sessions.CreateMobile(ctx, user, "Protected PDF reference")
			historyCheck(t, err)
			api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100})
			if scenario == "reversion" {
				path := fmt.Sprintf("/estimates/%d", document)
				reviewed := matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200))
				converted := matrixState(t, lifecycleCall(t, api, token, "POST", path+"/convert", matrixBody(t, map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": reviewed.Revision}), 200))
				document = converted.ID
				kind = "invoice"
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/__fixture/", func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(403)
					return
				}
				var err error
				switch r.URL.Path {
				case "/__fixture/fault":
					_, err = pool.Exec(ctx, `CREATE FUNCTION protected_pdf_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected publication outage'; END $$; CREATE TRIGGER protected_pdf_failure BEFORE INSERT ON document_pdf_versions FOR EACH STATEMENT EXECUTE FUNCTION protected_pdf_failure()`)
				case "/__fixture/repair":
					_, err = pool.Exec(ctx, `DROP TRIGGER protected_pdf_failure ON document_pdf_versions`)
				case "/__fixture/drift":
					_, err = pool.Exec(ctx, `UPDATE invoices SET notes='New reviewed source' WHERE id=$1`, document)
				case "/__fixture/archive":
					path := fmt.Sprintf("/invoices/%d", document)
					matrixTransition(t, api, token, path, j.ID, matrixState(t, lifecycleCall(t, api, token, "GET", path, "", 200)), "invoice:void", "status", 200)
					_, err = pool.Exec(ctx, `UPDATE invoices SET deleted_at=clock_timestamp() WHERE id=$1`, document)
				case "/__fixture/revoke":
					err = sessions.RevokeMobile(ctx, token)
				case "/__fixture/download-only":
					_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='invoice.pdf.generate'`, role)
				case "/__fixture/scope-loss":
					_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1 AND job_id=$2`, user, j.ID)
				case "/__fixture/restore":
					_, err = pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions; ALTER TABLE pdf_operations DISABLE TRIGGER protect_pdf_operation; DELETE FROM pdf_operations; ALTER TABLE pdf_operations ENABLE TRIGGER protect_pdf_operation`)
					historyCheck(t, err)
					fresh, err := sessions.CreateMobile(ctx, admin, "Restored PDF client")
					historyCheck(t, err)
					_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
					return
				default:
					w.WriteHeader(404)
					return
				}
				if err != nil {
					t.Errorf("fixture: %v", err)
					w.WriteHeader(500)
					return
				}
				w.WriteHeader(204)
			})
			mux.Handle("/", api)
			server := httptest.NewServer(mux)
			defer server.Close()
			directory := t.TempDir()
			historyCheck(t, os.Chmod(directory, 0700))
			input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "scenario": scenario, "kind": kind})
			historyCheck(t, err)
			bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(bounded, "node", "../api/v1/testdata/pdf-protected-http.cjs")
			cmd.Stdin = bytes.NewReader(input)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("protected PDF: %v\n%s", err, output)
			}
			t.Log(string(output))
		})
	}
}
