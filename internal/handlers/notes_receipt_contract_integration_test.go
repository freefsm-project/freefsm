package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/config"
)

func TestNotesReceiptTypedRecoveryHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Receipt owner").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Receipt contract").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Receipt contract")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/jobs/%d", j.ID)
	intent := executionNotes(t, api, token, path, "Attributable observation")
	if _, err = pool.Exec(ctx, `CREATE FUNCTION fail_notes_receipt_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='job.field_notes.edit' THEN RAISE EXCEPTION 'receipt test dependency failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_notes_receipt_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_notes_receipt_audit()`); err != nil {
		t.Fatal(err)
	}
	body := executionAPI(t, api, token, "POST", path+"/field-notes", intent, 503)
	var failure struct {
		Error struct {
			Status    int            `json:"status"`
			Code      string         `json:"code"`
			RequestID string         `json:"request_id"`
			Recovery  string         `json:"recovery_disposition"`
			Retry     int            `json:"retry_after_seconds"`
			Details   map[string]any `json:"details"`
		}
	}
	if err := json.Unmarshal([]byte(body), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Status != 503 || failure.Error.Code != "retryable_failure" || failure.Error.RequestID == "" || failure.Error.Recovery != "retry" || failure.Error.Retry < 1 || failure.Error.Details == nil {
		t.Fatalf("incomplete typed transient contract: %s", body)
	}
	executionAPI(t, api, token, "GET", "/job-execution-operations/"+intent["operation_id"].(string), nil, 404)
	if _, err = pool.Exec(ctx, `DROP TRIGGER fail_notes_receipt_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	accepted := executionAPI(t, api, token, "POST", path+"/field-notes", intent, 200)
	if got := executionAPI(t, api, token, "POST", path+"/field-notes", intent, 200); got != accepted {
		t.Fatal("retry duplicated receipt")
	}
	beforeHistory := executionAPI(t, api, token, "GET", path+"/field-notes/history", nil, 200)
	noop := executionNotes(t, api, token, path, "Attributable observation")
	noopReceipt := executionAPI(t, api, token, "POST", path+"/field-notes", noop, 200)
	var effect, sameEffect struct {
		Data struct {
			EffectID string `json:"effect_id"`
		}
	}
	if err := json.Unmarshal([]byte(accepted), &effect); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(noopReceipt), &sameEffect); err != nil {
		t.Fatal(err)
	}
	if effect.Data.EffectID == "" || effect.Data.EffectID != sameEffect.Data.EffectID {
		t.Fatal("no-op replaced accepted effect identity")
	}
	if got := executionAPI(t, api, token, "GET", path+"/field-notes/history", nil, 200); got != beforeHistory {
		t.Fatal("no-op changed original history/attribution")
	}
	intent["field_notes"] = "Changed intent must not execute"
	body = executionAPI(t, api, token, "POST", path+"/field-notes", intent, 409)
	if err := json.Unmarshal([]byte(body), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Status != 409 || failure.Error.Code != "operation_intent_changed" || failure.Error.Recovery != "review" {
		t.Fatalf("terminal recovery: %s", body)
	}
	throttled := executionNotes(t, api, token, path, "Unchanged after throttle")
	limited := apiv1.NewConfiguredRouter(pool, client, sessions, config.APIConfig{RequestsPerUser: 1})
	executionAPI(t, limited, token, "GET", path+"/field-notes", nil, 200)
	body = executionAPI(t, limited, token, "POST", path+"/field-notes", throttled, 429)
	if err := json.Unmarshal([]byte(body), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Status != 429 || failure.Error.Code != "rate_limit_exceeded" || failure.Error.Recovery != "retry" || failure.Error.Retry < 1 {
		t.Fatalf("throttle recovery: %s", body)
	}
	executionAPI(t, api, token, "GET", "/job-execution-operations/"+throttled["operation_id"].(string), nil, 404)
	executionAPI(t, api, token, "POST", path+"/field-notes", throttled, 200)
}
