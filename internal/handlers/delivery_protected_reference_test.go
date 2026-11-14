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
	for _, scenario := range []string{"create_lost", "retry_lost", "create_absent", "retry_absent", "dependency", "dependency_failed", "scope_receipt", "parent_loss", "retry_parent_loss", "denial", "retry_denial", "retry_invalid_state", "retry_cancelled", "revoked", "expiry", "restore"} {
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
			auth := authorization.New(pool)
			role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Protected Delivery role", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "estimate.view", Scope: authorization.Assigned}, {Permission: "estimate.delivery.request", Scope: authorization.Assigned}, {Permission: "estimate.pdf.download", Scope: authorization.Assigned}, {Permission: "estimate.pdf.generate", Scope: authorization.Assigned}})
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
			for _, name := range []string{"DeliveryMessage", "ReviewedDelivery", "DeliveryRetry", "DeliveryReceiptEnvelope", "DeliveryReviewEnvelope", "ErrorEnvelope"} {
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
				case "/__fixture/revoke":
					err = sessions.RevokeMobile(ctx, token)
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
					if strings.HasSuffix(r.URL.Path, "/review") {
						name = "DeliveryMessage"
					} else if r.URL.Path == "/delivery-retries" {
						name = "DeliveryRetry"
					}
					validate(name, raw)
				}
				recorder := httptest.NewRecorder()
				api.ServeHTTP(recorder, r)
				if recorder.Code != 200 {
					validate("ErrorEnvelope", recorder.Body.Bytes())
				} else if !strings.HasSuffix(r.URL.Path, "/review") {
					validate("DeliveryReceiptEnvelope", recorder.Body.Bytes())
				} else {
					validate("DeliveryReviewEnvelope", recorder.Body.Bytes())
				}
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
			input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "directory": directory, "scenario": scenario, "version": version})
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
		})
	}
}
