package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

// Frozen pre-exact rejection shape: the old float64 decoder accepted a nested
// null as zero, then stored an invalid_document receipt under numeric-zero intent.
// This is source-shaped history, not a claim of historical server execution.
func TestCommercialLegacyNullRejectionReplayAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Null history Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Null history Job").SaveX(ctx)
	actor := client.User.Create().SetCompanyID(company).SetName("Null history actor").SetEmail("null-history@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	var grants []authorization.Grant
	for _, permission := range []string{"invoice.view", "invoice.create", "estimate.view", "estimate.create", "pricing.view", "customer.view", "job.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Null history author", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, actor.ID, role); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, actor.ID, "Null history device")
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM sessions WHERE user_id=$1 AND kind='mobile'`, actor.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	type command struct{ kind, operation, body string }
	var old, current []command
	assignFieldDocumentCreator(t, pool, j.ID, actor.ID)
	for _, kind := range []string{"estimate", "invoice"} {
		op, identity := newOperationKey(), newOperationKey()
		intent := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Historical rejected quantity","tax_rate":"0","line_items":[{"title":"Work","quantity":0,"unit_price":"1.00","taxable":false}]}`, op, identity, j.ID)
		receipt := fmt.Sprintf(`{"operation_id":%q,"outcome":"invalid_document"}`, op)
		if _, err := pool.Exec(ctx, `INSERT INTO commercial_operations(user_id,client_hash,operation_id,document_type,requested_id,intent,receipt,intent_version) VALUES($1,$2,$3,$4,0,$5,$6,0)`, actor.ID, hash, op, kind, intent, receipt); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO operation_admissions(operation_id,deployment_id,user_id,client_key,operation_type,intent) SELECT $1,id,$2,$3,$4,jsonb_build_object('id',0,'intent',$5::jsonb) FROM operation_deployment`, op, actor.ID, "api:"+hash, "commercial."+kind, intent); err != nil {
			t.Fatal(err)
		}
		old = append(old, command{kind, op, strings.Replace(intent, `"quantity":0`, `"quantity":null`, 1)})
		newOp := newOperationKey()
		body := strings.Replace(strings.Replace(old[len(old)-1].body, op, newOp, 1), identity, newOperationKey(), 1)
		body = strings.Replace(body, `"job_id":`, fmt.Sprintf(`"customer_id":%d,"job_id":`, c.ID), 1)
		current = append(current, command{kind, newOp, body})
	}
	snapshot := func() string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(o) ORDER BY operation_id) FROM commercial_operations o WHERE intent_version=0),(SELECT jsonb_agg(to_jsonb(a) ORDER BY operation_id) FROM operation_admissions a WHERE operation_id IN (SELECT operation_id FROM commercial_operations WHERE intent_version=0)))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	for _, cmd := range append(append([]command{}, old...), current...) {
		first := call("POST", "/"+cmd.kind+"s", cmd.body, 422)
		assertContains(t, first, `"code":"invalid_document"`)
		call("POST", "/"+cmd.kind+"s", cmd.body, 422)
		var receipt struct {
			Data authorization.CommercialReceipt `json:"data"`
		}
		if err := json.Unmarshal([]byte(call("GET", "/commercial-operations/"+cmd.operation, "", 200)), &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Data.OperationID != cmd.operation || receipt.Data.Outcome != "invalid_document" || receipt.Data.ID != 0 {
			t.Fatalf("wrong retained rejection: %+v", receipt.Data)
		}
		call("POST", "/"+cmd.kind+"s", strings.Replace(cmd.body, `"quantity":null`, `"quantity":1`, 1), 409)
	}
	for _, cmd := range current {
		// Only historical normalization maps null to zero. New rejected intents
		// retain the original null wire value and do not equate a changed zero.
		call("POST", "/"+cmd.kind+"s", strings.Replace(cmd.body, `"quantity":null`, `"quantity":0`, 1), 409)
		var exact bool
		if err := pool.QueryRow(ctx, `SELECT intent_version=1 AND jsonb_typeof(intent->'line_items'->0->'quantity')='null' FROM commercial_operations WHERE operation_id=$1`, cmd.operation).Scan(&exact); err != nil {
			t.Fatal(err)
		}
		if !exact {
			t.Fatal("new null rejection lost its exact intent")
		}
	}
	empty, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Null history no access", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, actor.ID, empty); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range append(old, current...) {
		call("POST", "/"+cmd.kind+"s", cmd.body, 422)
		body := call("GET", "/commercial-operations/"+cmd.operation, "", 200)
		for _, secret := range []string{"Historical rejected quantity", "line_items", "unit_price"} {
			assertNotContains(t, body, secret)
		}
	}
	if snapshot() != before {
		t.Fatal("historical intents, receipts or admissions changed")
	}
	var noEffects bool
	if err := pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM invoices) AND NOT EXISTS(SELECT 1 FROM estimates) AND NOT EXISTS(SELECT 1 FROM issued_document_numbers) AND NOT EXISTS(SELECT 1 FROM commercial_client_identities) AND NOT EXISTS(SELECT 1 FROM authorization_audit WHERE action IN ('invoice.create','estimate.create'))`).Scan(&noEffects); err != nil {
		t.Fatal(err)
	}
	if !noEffects {
		t.Fatal("null rejection or replay published a commercial effect")
	}
}
