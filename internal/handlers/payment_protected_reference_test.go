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
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestPaymentProtectedReferenceHTTP(t *testing.T) {
	for _, scenario := range []string{"lost", "scope_loss", "grant_loss", "new_session", "absent", "transient", "terminal", "rename", "expiry", "revoked", "restore", "restore_completed_existing", "restore_completed_missing", "restore_completed_repeated", "restore_completed_scope_loss", "restore_completed_grant_loss", "restore_terminal_missing", "restore_purge_before_lookup", "restore_purge_after_lookup", "restore_purge_after_ack", "restore_purge_other_user", "ordinary_purge_missing", "generation_session_missing", "generation_grant_missing", "generation_terminal_missing", "generation_existing", "generation_other_user", "invalid_receipt", "tampered_store"} {
		t.Run(scenario, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("PRIVATE-PAYMENT-CUSTOMER").SaveX(ctx)
			job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Collection").SaveX(ctx)
			assignFieldDocumentCreator(t, pool, job.ID, admin)
			adminToken, err := sessions.CreateMobile(ctx, admin, "Payment setup")
			historyCheck(t, err)
			api := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100})
			raw := lifecycleCall(t, api, adminToken, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"PRIVATE-PAYMENT-INVOICE","line_items":[{"title":"Work","quantity":"1","unit_price":"100.00"}]}`, newOperationKey(), newOperationKey(), job.ID, customer.ID), 200)
			var created struct{ Data struct{ ID int64 } }
			historyCheck(t, json.Unmarshal([]byte(raw), &created))
			historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, created.Data.ID))
			auth := authorization.New(pool)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Protected collector", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "payment.record", Scope: authorization.Assigned}})
			historyCheck(t, err)
			user := client.User.Create().SetCompanyID(company).SetEmail("protected-payment@test").SetName("Collector").SetRole("tech").SetPasswordHash("fixture").SaveX(ctx)
			historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, user.ID, role))
			assignFieldDocumentCreator(t, pool, job.ID, user.ID)
			token, err := sessions.CreateMobile(ctx, user.ID, "Protected Payment")
			historyCheck(t, err)
			mux := http.NewServeMux()
			mux.HandleFunc("/__fixture/", func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(403)
					return
				}
				var err error
				switch r.URL.Path {
				case "/__fixture/scope-loss":
					_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job.ID, user.ID)
				case "/__fixture/grant-loss":
					_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='payment.record'`, role)
				case "/__fixture/grant-regain":
					var revision int64
					err = pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision)
					if err == nil {
						_, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Protected collector", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "payment.record", Scope: authorization.Assigned}}, revision)
					}
				case "/__fixture/session-loss":
					_, err = pool.Exec(ctx, `DELETE FROM sessions`)
				case "/__fixture/rename":
					_, err = pool.Exec(ctx, `INSERT INTO payment_method_labels(company_id,method,label) VALUES($1,'cash','Renamed cash') ON CONFLICT(company_id,method) DO UPDATE SET label=excluded.label`, company)
				case "/__fixture/fail":
					_, err = pool.Exec(ctx, `CREATE FUNCTION protected_payment_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'receipt fault'; END $$; CREATE TRIGGER protected_payment_failure BEFORE INSERT ON bounded_payment_operations FOR EACH ROW EXECUTE FUNCTION protected_payment_failure()`)
				case "/__fixture/recover":
					_, err = pool.Exec(ctx, `DROP TRIGGER protected_payment_failure ON bounded_payment_operations; DROP FUNCTION protected_payment_failure()`)
				case "/__fixture/revoke":
					err = sessions.RevokeMobile(ctx, token)
				case "/__fixture/forget-receipt":
					// Deliberate receipt-loss simulation, not a database restore. The
					// real backup journey is covered by the backup reconciliation test.
					_, err = pool.Exec(ctx, `ALTER TABLE bounded_payment_operations DISABLE TRIGGER USER; DELETE FROM bounded_payment_operations; ALTER TABLE bounded_payment_operations ENABLE TRIGGER USER`)
				case "/__fixture/restore", "/__fixture/new-session", "/__fixture/other-user":
					if r.URL.Path == "/__fixture/restore" {
						_, err = pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`)
					}
					if err == nil {
						var fresh string
						subject := user.ID
						if r.URL.Path == "/__fixture/other-user" {
							subject = admin
						}
						fresh, err = sessions.CreateMobile(ctx, subject, "Recovered Payment")
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
			specResponse := httptest.NewRecorder()
			api.ServeHTTP(specResponse, httptest.NewRequest("GET", "/openapi.json", nil))
			var spec map[string]any
			historyCheck(t, json.Unmarshal(specResponse.Body.Bytes(), &spec))
			compiler := jsonschema.NewCompiler()
			compiler.AssertFormat()
			historyCheck(t, compiler.AddResource("https://payment.test/openapi.json", spec))
			commandSchema, err := compiler.Compile("https://payment.test/openapi.json#/components/schemas/BoundedPaymentCommand")
			historyCheck(t, err)
			mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				path := r.URL.Path
				if strings.HasSuffix(path, "/payments") {
					path = "/invoices/{id}/payments"
					raw, err := io.ReadAll(r.Body)
					historyCheck(t, err)
					r.Body = io.NopCloser(bytes.NewReader(raw))
					value, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
					historyCheck(t, err)
					if err := commandSchema.Validate(value); err != nil {
						t.Errorf("Payment request contract: %v", err)
					}
				} else if strings.HasPrefix(path, "/payment-operations/") {
					path = "/payment-operations/{operation}"
				} else if strings.HasSuffix(path, "/payment-methods") {
					path = "/invoices/{id}/payment-methods"
				} else {
					api.ServeHTTP(w, r)
					return
				}
				recorder := httptest.NewRecorder()
				api.ServeHTTP(recorder, r)
				deliveryResponseContract(t, spec, compiler, "https://payment.test/openapi.json", path, r.Method, recorder.Code, recorder.Body.Bytes())
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
			key := newOperationKey()
			// Own the native key outside the child so assertion/timeout failures also
			// clean it. The child has exited before cleanup can touch this store.
			t.Cleanup(func() {
				cleanup, err := json.Marshal(map[string]any{"directory": directory, "key": key, "cleanup": true})
				historyCheck(t, err)
				bounded, cancel := context.WithTimeout(ctx, time.Minute)
				defer cancel()
				cmd := exec.CommandContext(bounded, "node", "../api/v1/testdata/payment-protected-http.cjs")
				cmd.Stdin = bytes.NewReader(cleanup)
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Errorf("owned Payment key cleanup: %v\n%s", err, output)
				}
			})
			input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "key": key, "scenario": scenario})
			historyCheck(t, err)
			bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(bounded, "node", "../api/v1/testdata/payment-protected-http.cjs")
			cmd.Stdin = bytes.NewReader(input)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("protected Payment: %v\n%s", err, output)
			}
			t.Log(string(output))
			want := 1
			if scenario == "absent" || scenario == "ordinary_purge_missing" || scenario == "generation_terminal_missing" || scenario == "expiry" || scenario == "revoked" || scenario == "tampered_store" {
				want = 0
			}
			var payments, credits int
			var amount int64
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*),coalesce(sum(amount_cents),0) FROM invoice_payments WHERE invoice_id=$1`, created.Data.ID).Scan(&payments, &amount))
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM customer_credits`).Scan(&credits))
			if payments != want || amount != int64(want)*10000 || credits != 0 {
				t.Fatalf("effects: payments=%d amount=%d credits=%d", payments, amount, credits)
			}
			if scenario == "rename" {
				var method, label, reference, note string
				historyCheck(t, pool.QueryRow(ctx, `SELECT method,method_label,reference,notes FROM invoice_payments WHERE invoice_id=$1`, created.Data.ID).Scan(&method, &label, &reference, &note))
				if method != "cash" || label != "Renamed cash" || reference != "PRIVATE-PAYMENT-REFERENCE" || note != "PRIVATE-PAYMENT-NOTE" {
					t.Fatal("method rename changed immutable intent")
				}
			}
		})
	}
}
