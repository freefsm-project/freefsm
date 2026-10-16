package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func lifecycleCall(t *testing.T, api http.Handler, token, method, path, body string, want int) string {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s=%d want%d: %s", method, path, w.Code, want, w.Body.String())
	}
	specResponse := httptest.NewRecorder()
	api.ServeHTTP(specResponse, httptest.NewRequest("GET", "/openapi.json", nil))
	var spec map[string]any
	if err := json.Unmarshal(specResponse.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("https://lifecycle.test/openapi.json", spec); err != nil {
		t.Fatal(err)
	}
	validate := func(name, raw string) {
		t.Helper()
		schema, err := compiler.Compile("https://lifecycle.test/openapi.json#/components/schemas/" + name)
		if err != nil {
			t.Fatal(err)
		}
		value, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(value); err != nil {
			t.Fatalf("%s contract: %v", name, err)
		}
	}
	if strings.HasSuffix(path, "/status") || strings.HasSuffix(path, "/finalize") {
		validate("CommercialLifecycleCommand", body)
	}
	name := "CommercialReceiptEnvelope"
	if want >= 400 {
		name = "ErrorEnvelope"
	} else if method == "GET" && !strings.HasPrefix(path, "/commercial-operations/") {
		name = "CommercialReadEnvelope"
	}
	validate(name, w.Body.String())
	return w.Body.String()
}

func TestCommercialLifecycleAuditFailureIsRetryableAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Atomic lifecycle Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Atomic lifecycle Job").SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Lifecycle failure")
	if err != nil {
		t.Fatal(err)
	}
	created := lifecycleCall(t, api, token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Atomic lifecycle","line_items":[]}`, newOperationKey(), newOperationKey(), j.ID), 200)
	var receipt struct {
		Data struct {
			ID       int64  `json:"id"`
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(created), &receipt); err != nil {
		t.Fatal(err)
	}
	var status int64
	var meaning string
	if err := pool.QueryRow(ctx, `SELECT id,meaning_revision::text FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced'`, company).Scan(&status, &meaning); err != nil {
		t.Fatal(err)
	}
	op := newOperationKey()
	path := fmt.Sprintf("/invoices/%d", receipt.Data.ID)
	body := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"status_id":%d,"meaning_revision":%q}`, op, j.ID, receipt.Data.Revision, status, meaning)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION lifecycle_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='invoice.finalize' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='injected lifecycle audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER lifecycle_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION lifecycle_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	lifecycleCall(t, api, token, "POST", path+"/finalize", body, 503)
	lifecycleCall(t, api, token, "GET", "/commercial-operations/"+op, "", 404)
	assertContains(t, lifecycleCall(t, api, token, "GET", path, "", 200), `"manual_category":"invoice:draft"`)
	if _, err := pool.Exec(ctx, `DROP TRIGGER lifecycle_audit_failure ON authorization_audit; DROP FUNCTION lifecycle_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	lifecycleCall(t, api, token, "POST", path+"/finalize", body, 200)
	lifecycleCall(t, api, token, "POST", path+"/finalize", body, 200)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='invoice.finalize' AND target_id=$1`, receipt.Data.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("retry audit count=%d", count)
	}
}

func TestCommercialLifecycleSettlementVoidRaceAPI(t *testing.T) {
	for _, paymentFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(paymentFirst), func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Void race Customer").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Void race Job").SaveX(ctx)
			api := apiv1.NewRouter(pool, client, sessions)
			token, err := sessions.CreateMobile(ctx, admin, "Void race")
			if err != nil {
				t.Fatal(err)
			}
			created := lifecycleCall(t, api, token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Void race","line_items":[{"title":"Work","quantity":"1","unit_price":"100.00"}]}`, newOperationKey(), newOperationKey(), j.ID), 200)
			var receipt struct {
				Data struct {
					ID       int64  `json:"id"`
					Revision string `json:"revision"`
				} `json:"data"`
			}
			if err := json.Unmarshal([]byte(created), &receipt); err != nil {
				t.Fatal(err)
			}
			id := receipt.Data.ID
			path := fmt.Sprintf("/invoices/%d", id)
			if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, id); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(lifecycleCall(t, api, token, "GET", path, "", 200)), &receipt); err != nil {
				t.Fatal(err)
			}
			var status int64
			var meaning string
			if err := pool.QueryRow(ctx, `SELECT id,meaning_revision::text FROM statuses WHERE company_id=$1 AND category_key='invoice:void'`, company).Scan(&status, &meaning); err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"status_id":%d,"meaning_revision":%q}`, newOperationKey(), j.ID, receipt.Data.Revision, status, meaning)
			fence, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer fence.Rollback(context.Background())
			var blocker int
			if err := fence.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) barrier`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			paid := make(chan error, 1)
			voided := make(chan *httptest.ResponseRecorder, 1)
			payment := func() {
				_, err := settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: id, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: settlement.Date(time.Now().Format("2006-01-02"))})
				paid <- err
			}
			void := func() {
				r := httptest.NewRequest("POST", path+"/status", strings.NewReader(body)).WithContext(ctx)
				r.Header.Set("Authorization", "Bearer "+token)
				r.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				api.ServeHTTP(w, r)
				voided <- w
			}
			if paymentFirst {
				go payment()
			} else {
				go void()
			}
			commercialWaiters(t, ctx, pool, blocker, 1)
			if paymentFirst {
				go void()
			} else {
				go payment()
			}
			commercialWaiters(t, ctx, pool, blocker, 2)
			if err := fence.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			paymentErr, w := <-paid, <-voided
			if paymentFirst && (paymentErr != nil || w.Code != 409) || !paymentFirst && (paymentErr == nil || w.Code != 200) {
				t.Fatalf("incompatible race outcomes: payment=%v void=%d %s", paymentErr, w.Code, w.Body.String())
			}
			state := lifecycleCall(t, api, token, "GET", path, "", 200)
			if paymentFirst {
				assertContains(t, state, `"effective_category":"invoice:partially_paid"`)
				assertContains(t, state, `"manual_category":"invoice:invoiced"`)
			} else {
				assertContains(t, state, `"effective_category":"invoice:void"`)
				assertContains(t, state, `"settlement_locked":false`)
			}
		})
	}
}

func TestCommercialLifecycleSemanticReviewAndIndependentGrantAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Semantic Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Semantic Job").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Workflow only").SetEmail("lifecycle-only@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	grants := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.status", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Workflow only", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	adminToken, err := sessions.CreateMobile(ctx, admin, "Lifecycle creator")
	if err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, u.ID, "Workflow only")
	if err != nil {
		t.Fatal(err)
	}
	created := lifecycleCall(t, api, adminToken, "POST", "/estimates", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Semantic Estimate","line_items":[]}`, newOperationKey(), newOperationKey(), j.ID), 200)
	var effect struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(created), &effect); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/estimates/%d", effect.Data.ID)
	svc := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	choice, err := svc.Create(ctx, actor, statusflow.CreateRequest{Type: statusflow.Estimate, Name: "Reviewed acceptance", Category: statusflow.EstimateAccepted})
	if err != nil {
		t.Fatal(err)
	}
	read := func() (string, string) {
		t.Helper()
		var state struct {
			Data struct {
				Revision string `json:"revision"`
				Workflow []struct {
					ID      int64  `json:"id"`
					Meaning string `json:"meaning_revision"`
				} `json:"workflow"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(lifecycleCall(t, api, token, "GET", path, "", 200)), &state); err != nil {
			t.Fatal(err)
		}
		for _, v := range state.Data.Workflow {
			if v.ID == choice.ID {
				return state.Data.Revision, v.Meaning
			}
		}
		t.Fatal("missing reviewed workflow target")
		return "", ""
	}
	revision, meaning := read()
	intent := func(op, revision, meaning string) string {
		return fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"status_id":%d,"meaning_revision":%q}`, op, j.ID, revision, choice.ID, meaning)
	}
	op := newOperationKey()
	body := intent(op, revision, meaning)
	if _, err := svc.Update(ctx, actor, choice.ID, "Cosmetic acceptance rename", "#123456"); err != nil {
		t.Fatal(err)
	}
	accepted := lifecycleCall(t, api, token, "POST", path+"/status", body, 200)
	lifecycleCall(t, api, token, "PATCH", path, fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"notes":"No Edit grant"}`, newOperationKey(), j.ID, revision), 403)
	revision, meaning = read()
	if err := svc.Move(ctx, actor, statusflow.MoveRequest{StatusID: choice.ID, Category: statusflow.EstimateRejected, ConfirmInUse: true}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Move(ctx, actor, statusflow.MoveRequest{StatusID: choice.ID, Category: statusflow.EstimateAccepted, ConfirmInUse: true}); err != nil {
		t.Fatal(err)
	}
	lifecycleCall(t, api, token, "POST", path+"/status", intent(newOperationKey(), revision, meaning), 409)
	var auditOK bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE action='estimate.status' AND target_id=$1 AND channel='api' AND detail->>'operation_id'=$2 AND detail ? 'accepted_at' AND detail ? 'occurred_at' AND detail ? 'session_id')`, effect.Data.ID, op).Scan(&auditOK); err != nil {
		t.Fatal(err)
	}
	if !auditOK {
		t.Fatal("lifecycle audit lacks operation, occurrence/acceptance or device attribution")
	}
	empty, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "No document access", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, empty); err != nil {
		t.Fatal(err)
	}
	lifecycleCall(t, api, token, "GET", path, "", 404)
	if replay := lifecycleCall(t, api, token, "POST", path+"/status", body, 200); replay != accepted {
		t.Fatal("lost-access replay changed accepted effect")
	}
	lifecycleCall(t, api, token, "GET", "/commercial-operations/"+op, "", 200)
}
