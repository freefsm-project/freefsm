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
	"github.com/freefsm-project/freefsm/internal/settlement"
)

func TestConversionProtectedReferenceAPI(t *testing.T) {
	for _, scenario := range []string{"history", "fields", "origin_refund", "settlement", "revoke", "restore", "repeated_restore_contact", "repeated_restore_lease", "authority_convert", "authority_assignment", "authority_archive", "authority_input"} {
		t.Run(scenario, func(t *testing.T) { conversionProtectedReference(t, scenario) })
	}
}

func TestConversionProtectedBatchAPI(t *testing.T) {
	for _, scenario := range []string{"batch_history", "batch_transient", "batch_interruption", "batch_restore"} {
		t.Run(scenario, func(t *testing.T) { conversionProtectedReference(t, scenario) })
	}
}

func conversionProtectedReference(t *testing.T, scenario string) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Protected conversion").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Protected conversion").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, job.ID, admin)
	token, err := sessions.CreateMobile(ctx, admin, "Protected conversion")
	historyCheck(t, err)
	creationToken := token
	var role, scopedUser int64
	if strings.HasPrefix(scenario, "authority_") {
		auth := authorization.New(pool)
		u := client.User.Create().SetCompanyID(company).SetName("Scoped conversion").SetEmail("protected-conversion@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
		scopedUser = u.ID
		role, err = auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Scoped conversion", []authorization.Grant{{Permission: "job.view", Scope: authorization.Assigned}, {Permission: "estimate.view", Scope: authorization.Assigned}, {Permission: "estimate.create", Scope: authorization.Assigned}, {Permission: "estimate.convert", Scope: authorization.Assigned}, {Permission: "invoice.view", Scope: authorization.Assigned}, {Permission: "invoice.create", Scope: authorization.Assigned}, {Permission: "pricing.view", Scope: authorization.Assigned}})
		historyCheck(t, err)
		historyCheck(t, auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role))
		assignFieldDocumentCreator(t, pool, job.ID, u.ID)
		token, err = sessions.CreateMobile(ctx, u.ID, "Scoped conversion")
		historyCheck(t, err)
	}
	api := conversionContract(t, apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{SyncPerClient: 100}))
	fields := map[string]int64{}
	var payment, refund settlement.Result
	if scenario == "fields" {
		for _, f := range []struct {
			key, kind, pairing string
			required           bool
		}{
			{"source", "estimate", "captured", false}, {"paired", "invoice", "captured", false}, {"original", "estimate", "", false}, {"entered", "invoice", "", true},
		} {
			var id int64
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,conversion_key,required) VALUES($1,$2,$3,'text',NULLIF($4,''),$5) RETURNING id`, company, f.kind, f.key, f.pairing, f.required).Scan(&id))
			fields[f.key] = id
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fixture/fault" {
			var in struct{ ID string }
			historyCheck(t, json.NewDecoder(r.Body).Decode(&in))
			_, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION conversion_batch_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation_id='%s'::uuid THEN RAISE EXCEPTION 'conversion receipt failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER conversion_batch_failure BEFORE INSERT ON commercial_operations FOR EACH ROW EXECUTE FUNCTION conversion_batch_failure()`, in.ID))
			historyCheck(t, err)
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/__fixture/clear" {
			_, err := pool.Exec(ctx, `DROP TRIGGER conversion_batch_failure ON commercial_operations`)
			historyCheck(t, err)
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/sync/operations" && r.Header.Get("X-Batch-Interrupt") == "yes" {
			var body map[string][]json.RawMessage
			historyCheck(t, json.NewDecoder(r.Body).Decode(&body))
			body["operations"] = body["operations"][:1]
			raw, err := json.Marshal(body)
			historyCheck(t, err)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			response := httptest.NewRecorder()
			api.ServeHTTP(response, r)
			if response.Code != 200 {
				t.Errorf("prefix status %d: %s", response.Code, response.Body.String())
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			historyCheck(t, err)
			historyCheck(t, conn.Close())
			return
		}
		if r.URL.Path != "/__fixture/change" {
			api.ServeHTTP(w, r)
			return
		}
		switch scenario {
		case "origin_refund", "settlement":
			var in struct {
				Invoice int64  `json:"invoice"`
				Action  string `json:"action"`
			}
			historyCheck(t, json.NewDecoder(r.Body).Decode(&in))
			svc := settlement.New(pool)
			actor := settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
			date := settlement.Date(time.Now().Format("2006-01-02"))
			switch in.Action {
			case "payment":
				payment, err = svc.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: in.Invoice, AmountCents: 5000, Method: settlement.Cash, ReceivedDate: date})
			case "refund":
				refund, err = svc.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, CustomerID: customer.ID, AmountCents: 5000, Method: settlement.Cash, EffectiveDate: date, Reason: "Explicit refund"})
			case "reverse_refund":
				_, err = svc.ReverseRefund(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: refund.ID, CustomerID: customer.ID, EffectiveDate: date, Reason: "Explicit unwind"})
			case "reverse_payment":
				_, err = svc.ReversePayment(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: payment.ID, InvoiceID: in.Invoice, EffectiveDate: date, Reason: "error"})
			default:
				t.Errorf("unknown financial fixture action %s", in.Action)
			}
		case "fields":
			_, err = pool.Exec(ctx, `UPDATE custom_field_definitions SET retired=true,conversion_key=NULL WHERE id IN ($1,$2)`, fields["source"], fields["paired"])
			historyCheck(t, err)
			var required int64
			historyCheck(t, pool.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,required,default_value) VALUES($1,'estimate','Required after capture','text',true,'Never backfill') RETURNING id`, company).Scan(&required))
			_ = json.NewEncoder(w).Encode(map[string]int64{"required": required})
			return
		case "revoke":
			_, err = pool.Exec(ctx, `DELETE FROM sessions`)
		case "authority_convert":
			_, err = pool.Exec(ctx, `DELETE FROM authorization_grants WHERE role_id=$1 AND permission='estimate.convert'`, role)
		case "authority_assignment":
			_, err = pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1 AND job_id=$2`, scopedUser, job.ID)
		case "authority_archive":
			_, err = pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp() WHERE id=$1`, job.ID)
		case "authority_input":
			// Existing Convert/Create authority never implies Invoice Edit for
			// entered values, even an explicitly supplied empty field selection.
		default:
			_, err = pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); DELETE FROM sessions`)
			historyCheck(t, err)
			fresh, e := sessions.CreateMobile(ctx, admin, "Restored conversion")
			historyCheck(t, e)
			_ = json.NewEncoder(w).Encode(map[string]string{"token": fresh})
			return
		}
		historyCheck(t, err)
		w.WriteHeader(204)
	}))
	defer server.Close()
	directory := t.TempDir()
	historyCheck(t, os.Chmod(directory, 0700))
	input, err := json.Marshal(map[string]any{"url": server.URL, "token": token, "creationToken": creationToken, "directory": directory, "job": job.ID, "customer": customer.ID, "scenario": scenario, "fields": fields})
	historyCheck(t, err)
	harness := "../api/v1/testdata/conversion-protected-http.cjs"
	if strings.HasPrefix(scenario, "batch_") {
		harness = "../api/v1/testdata/conversion-batch-protected-http.cjs"
	}
	cmd := exec.CommandContext(ctx, "node", harness)
	cmd.Stdin = bytes.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("protected conversion: %v\n%s", err, out)
	}
	t.Log(string(out))
}
