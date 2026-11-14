package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestDeliveryProtectedReferenceHTTP(t *testing.T) {
	for _, scenario := range []string{"create_lost", "retry_lost", "create_absent", "retry_absent", "dependency", "dependency_failed", "scope_receipt", "parent_loss", "retry_parent_loss", "denial", "retry_denial", "retry_invalid_state", "retry_cancelled", "revoked", "expiry", "restore", "authority_view_only", "authority_no_delivery", "authority_no_pdf", "authority_no_file", "authority_permitted", "authority_file_loss", "authority_pdf_loss", "retry_sync_denial", "retry_scope_receipt", "retry_unknown_sync_denial", "retry_expiry", "retry_renewal", "retry_session", "retry_restore"} {
		t.Run(scenario, func(t *testing.T) {
			pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Protected Delivery").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Work").SaveX(ctx)
			doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("PRIVATE-DELIVERY-DOCUMENT").SaveX(ctx)
			path := fmt.Sprintf("/estimates/%d", doc.ID)
			cookie := sessionCookie(t, ctx, sessions, admin)
			_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
			historyCheck(t, err)
			expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
			version, err := strconv.Atoi(compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version"))
			historyCheck(t, err)
			var frozenDelivery int64
			var frozenSnapshot string
			frozenSQL := `SELECT jsonb_build_array(sender_address,recipients_to,recipients_cc,recipients_bcc,subject,text_body,html_body,pdf_data,pdf_filename,message_id,pdf_version_id,attachment_snapshots,composition_snapshot,lifetime_attempt_count,cycle_attempt_count)::text FROM document_deliveries WHERE id=$1`
			if strings.HasPrefix(scenario, "authority_") {
				conversionUpload(t, web, cookie, "estimate", doc.ID, "frozen.txt", "PRIVATE-FROZEN-EXTRA")
				var file int64
				var filePath string
				historyCheck(t, pool.QueryRow(ctx, `SELECT id,file_path FROM files WHERE object_type='estimate' AND object_id=$1`, doc.ID).Scan(&file, &filePath))
				fieldPost(t, web, cookie, path+"/email", url.Values{"pdf_version": {fmt.Sprint(version)}, "idempotency_key": {newOperationKey()}, "to": {"frozen@example.test"}, "subject": {"Frozen"}, "body": {"Frozen body"}, "extra_file_id": {fmt.Sprint(file)}}, 303)
				_, err = delivery.New(pool, "").ProcessOne(ctx, deliveryTestSender(func(context.Context, delivery.Delivery) (delivery.SendResult, error) {
					return delivery.SendResult{}, &delivery.SendError{Kind: delivery.SendPermanent, Err: errors.New("fixture failure")}
				}), nil)
				historyCheck(t, err)
				historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM document_deliveries`).Scan(&frozenDelivery))
				historyCheck(t, pool.QueryRow(ctx, frozenSQL, frozenDelivery).Scan(&frozenSnapshot))
				_, err = pool.Exec(ctx, `UPDATE files SET removed_at=clock_timestamp(),removed_by=$2 WHERE id=$1`, file, admin)
				historyCheck(t, err)
				historyCheck(t, os.Remove(filePath)) // Review/retry must use the frozen extra.
			}
			auth := authorization.New(pool)
			grants := []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "estimate.view", Scope: authorization.Assigned}}
			if scenario != "authority_view_only" && scenario != "authority_no_delivery" {
				grants = append(grants, authorization.Grant{Permission: "estimate.delivery.request", Scope: authorization.Assigned})
			}
			if scenario != "authority_view_only" && scenario != "authority_no_pdf" {
				grants = append(grants, authorization.Grant{Permission: "estimate.pdf.download", Scope: authorization.Assigned})
			}
			if !strings.HasPrefix(scenario, "authority_") {
				grants = append(grants, authorization.Grant{Permission: "estimate.pdf.generate", Scope: authorization.Assigned})
			}
			if scenario == "authority_permitted" || scenario == "authority_file_loss" || scenario == "authority_pdf_loss" {
				grants = append(grants, authorization.Grant{Permission: "file.download", Scope: authorization.Assigned})
			}
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Protected Delivery role", grants)
			historyCheck(t, err)
			user := client.User.Create().SetCompanyID(company).SetEmail("protected-delivery@test").SetName("Field").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role))
			assignFieldDocumentCreator(t, pool, job.ID, user.ID)
			token, err := sessions.CreateMobile(ctx, user.ID, "Protected Delivery reference")
			historyCheck(t, err)
			api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100})
			specRequest := httptest.NewRecorder()
			api.ServeHTTP(specRequest, httptest.NewRequest("GET", "/openapi.json", nil))
			var spec map[string]any
			historyCheck(t, json.Unmarshal(specRequest.Body.Bytes(), &spec))
			compiler := jsonschema.NewCompiler()
			compiler.AssertFormat()
			historyCheck(t, compiler.AddResource("https://delivery.test/openapi.json", spec))
			schemas := map[string]*jsonschema.Schema{}
			for _, name := range []string{"DeliveryMessage", "ReviewedDelivery", "DeliveryRetry", "DeliveryRetryTarget", "DeliveryReceiptEnvelope", "DeliveryReviewEnvelope", "ErrorEnvelope"} {
				schemas[name], err = compiler.Compile("https://delivery.test/openapi.json#/components/schemas/" + name)
				historyCheck(t, err)
			}
			validate := func(name string, raw []byte) {
				t.Helper()
				value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
				if err == nil {
					err = schemas[name].Validate(value)
				}
				if err != nil {
					t.Errorf("own OpenAPI %s: %v", name, err)
				}
			}
			mux := http.NewServeMux()
			mux.HandleFunc("/__fixture/", func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(403)
					return
				}
				var err error
				switch r.URL.Path {
				case "/__fixture/fail":
					_, err = delivery.New(pool, "").ProcessOne(ctx, deliveryTestSender(func(context.Context, delivery.Delivery) (delivery.SendResult, error) {
						return delivery.SendResult{}, &delivery.SendError{Kind: delivery.SendPermanent, Err: errors.New("permanent fixture failure")}
					}), nil)
				case "/__fixture/scope-loss":
					_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1 AND job_id=$2`, user.ID, job.ID)
				case "/__fixture/parent-loss":
					_, err = pool.Exec(ctx, `UPDATE estimates SET deleted_at=clock_timestamp() WHERE id=$1`, doc.ID)
				case "/__fixture/deny":
					_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='estimate.delivery.request'`, role)
				case "/__fixture/deny-file":
					_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='file.download'`, role)
				case "/__fixture/deny-pdf":
					_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='estimate.pdf.download'`, role)
				case "/__fixture/revoke":
					err = sessions.RevokeMobile(ctx, token)
				case "/__fixture/new-session":
					var fresh string
					fresh, err = sessions.CreateMobile(ctx, user.ID, "New Delivery client")
					if err == nil {
						_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
						return
					}
				case "/__fixture/cancel":
					fieldPost(t, web, cookie, path+"/delete", url.Values{}, 303)
					fieldPost(t, web, cookie, path+"/restore", url.Values{}, 303)
				case "/__fixture/restore":
					_, err = pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`)
					if err == nil {
						var fresh string
						fresh, err = sessions.CreateMobile(ctx, user.ID, "Restored Delivery client")
						if err == nil {
							_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
							return
						}
					}
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
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				isDelivery := strings.Contains(r.URL.Path, "/deliveries") || strings.HasPrefix(r.URL.Path, "/delivery-")
				if !isDelivery {
					api.ServeHTTP(w, r)
					return
				}
				if r.Method == "POST" {
					raw, err := io.ReadAll(r.Body)
					historyCheck(t, err)
					r.Body = io.NopCloser(bytes.NewReader(raw))
					name := "ReviewedDelivery"
					if r.URL.Path == "/delivery-retries/review" {
						name = "DeliveryRetryTarget"
					} else if strings.HasSuffix(r.URL.Path, "/review") {
						name = "DeliveryMessage"
					} else if r.URL.Path == "/delivery-retries" {
						name = "DeliveryRetry"
					}
					validate(name, raw)
				}
				recorder := httptest.NewRecorder()
				api.ServeHTTP(recorder, r)
				operationPath := r.URL.Path
				if strings.HasPrefix(operationPath, "/estimates/") {
					operationPath = "/estimates/{id}/deliveries"
					if strings.HasSuffix(r.URL.Path, "/review") {
						operationPath += "/review"
					}
				}
				if strings.HasPrefix(operationPath, "/delivery-operations/") {
					operationPath = "/delivery-operations/{operation}"
				}
				deliveryResponseContract(t, spec, compiler, "https://delivery.test/openapi.json", operationPath, r.Method, recorder.Code, recorder.Body.Bytes())
				for k, values := range recorder.Header() {
					w.Header()[k] = values
				}
				w.WriteHeader(recorder.Code)
				_, _ = w.Write(recorder.Body.Bytes())
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			directory := t.TempDir()
			historyCheck(t, os.Chmod(directory, 0700))
			input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "scenario": scenario, "version": version, "frozenDelivery": frozenDelivery})
			historyCheck(t, err)
			bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(bounded, "node", "../api/v1/testdata/delivery-protected-http.cjs")
			cmd.Stdin = bytes.NewReader(input)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("protected Delivery: %v\n%s", err, output)
			}
			t.Log(string(output))
			if frozenDelivery != 0 {
				var after string
				historyCheck(t, pool.QueryRow(ctx, frozenSQL, frozenDelivery).Scan(&after))
				if after != frozenSnapshot {
					t.Fatal("retry changed frozen message, PDF, extras or automatic budget")
				}
				var cycles int
				historyCheck(t, pool.QueryRow(ctx, `SELECT retry_cycle FROM document_deliveries WHERE id=$1`, frozenDelivery).Scan(&cycles))
				want := 0
				if scenario == "authority_permitted" {
					want = 1
				}
				if cycles != want {
					t.Fatalf("retry cycles %d want %d", cycles, want)
				}
				var admissions, receipts, effects int
				historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_admissions WHERE operation_type='delivery.retry'),(SELECT count(*) FROM delivery_operations),(SELECT count(*) FROM activity_logs WHERE action='document_delivery_retried')`).Scan(&admissions, &receipts, &effects))
				if admissions != want || receipts != want || effects != want {
					t.Fatalf("unexpected review/retry writes: admissions=%d receipts=%d effects=%d want=%d", admissions, receipts, effects, want)
				}
			}
		})
	}
}
