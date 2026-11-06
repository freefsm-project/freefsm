package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"net/http/httptest"
	"strings"
	"testing"
)

// Source-shaped history, frozen independently of today's request structs:
// base050dc612 services.CommercialLine.Quantity was float64 and its marshalled
// numeric value bound both commercial_operations and operation_admissions.
// This fixture does not claim execution of a historical server binary.
func TestCommercialLegacyNumericIntentReceiptReplayAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Historical receipt Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Historical receipt Job").SaveX(ctx)
	actor := client.User.Create().SetCompanyID(company).SetName("Historical commercial actor").SetEmail("legacy-commercial@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	var grants []authorization.Grant
	for _, permission := range []string{"invoice.view", "invoice.create", "invoice.edit", "pricing.view", "customer.view", "job.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Legacy commercial author", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, actor.ID, role); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, actor.ID, "Historical commercial device")
	if err != nil {
		t.Fatal(err)
	}
	var hash string
	if err := pool.QueryRow(ctx, `SELECT token_hash FROM sessions WHERE user_id=$1 AND kind='mobile'`, actor.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	var specification map[string]any
	historyCheck(t, json.Unmarshal(serviceAPIRequest(api, "", "GET", "/openapi.json", nil).Body.Bytes(), &specification))
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	historyCheck(t, compiler.AddResource("https://history.test/openapi.json", specification))
	createSchema, err := compiler.Compile("https://history.test/openapi.json#/components/requestBodies/CommercialCreate/content/application~1json/schema")
	historyCheck(t, err)
	call := func(method, path, body string, want int) string {
		t.Helper()
		if method == "POST" && path == "/invoices" {
			value, err := jsonschema.UnmarshalJSON(strings.NewReader(body))
			historyCheck(t, err)
			historyCheck(t, createSchema.Validate(value))
		}
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d want%d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	type retained struct {
		operation, body, receipt string
		id                       int64
	}
	var originals []retained
	for _, wireQuantity := range []string{"1.5", "1.5000000000000001"} {
		operation, identity := newOperationKey(), newOperationKey()
		var id int64
		var number string
		if err := pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,title,terms,tax_rate,line_items) VALUES($1,$2,$3,'Legacy quantity','Recorded terms','0','[{"title":"Captured","quantity":1.5,"unit_price":10}]') RETURNING id,number_label`, company, c.ID, j.ID).Scan(&id, &number); err != nil {
			t.Fatal(err)
		}
		revision, err := saveaction.Revision(ctx, pool, actor.ID, fmt.Sprintf("/invoices/%d", id))
		if err != nil {
			t.Fatal(err)
		}
		intent := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Legacy quantity","tax_rate":"0","line_items":[{"title":"Captured","quantity":1.5,"unit_price":"10.00","taxable":false}]}`, operation, identity, j.ID)
		body := strings.Replace(intent, `"quantity":1.5`, `"quantity":`+wireQuantity, 1)
		receipt := fmt.Sprintf(`{"operation_id":%q,"id":%d,"number":%q,"revision":%q,"outcome":"accepted"}`, operation, id, number, revision)
		if _, err := pool.Exec(ctx, `INSERT INTO commercial_client_identities(client_id,document_type,document_id,user_id,client_hash) VALUES($1,'invoice',$2,$3,$4)`, identity, id, actor.ID, hash); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO commercial_operations(user_id,client_hash,operation_id,document_type,requested_id,intent,receipt,intent_version) VALUES($1,$2,$3,'invoice',0,$4,$5,0)`, actor.ID, hash, operation, intent, receipt); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO operation_admissions(operation_id,deployment_id,user_id,client_key,operation_type,intent) SELECT $1,id,$2,$3,'commercial.invoice',jsonb_build_object('id',0,'intent',$4::jsonb) FROM operation_deployment`, operation, actor.ID, "api:"+hash, intent); err != nil {
			t.Fatal(err)
		}
		originals = append(originals, retained{operation, body, receipt, id})
	}
	for _, old := range originals {
		response := call("POST", "/invoices", old.body, 200)
		var envelope struct {
			Data authorization.CommercialReceipt `json:"data"`
		}
		if err := json.Unmarshal([]byte(response), &envelope); err != nil {
			t.Fatal(err)
		}
		var expected authorization.CommercialReceipt
		if err := json.Unmarshal([]byte(old.receipt), &expected); err != nil {
			t.Fatal(err)
		}
		if envelope.Data != expected {
			t.Fatal("historical receipt changed")
		}
		call("POST", "/invoices", strings.Replace(old.body, `"10.00"`, `"11.00"`, 1), 409)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(originals) {
		t.Fatal("historical replay created another document")
	}
	// New exact numeric intents must not inherit the legacy float equivalence.
	assignFieldDocumentCreator(t, pool, j.ID, actor.ID)
	missingBinding := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"New unbound intent"}`, newOperationKey(), newOperationKey(), j.ID)
	assertContains(t, call("POST", "/invoices", missingBinding, 428), "review_required")
	body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Exact numeric intent","line_items":[{"title":"Exact","quantity":1.50000000000000001,"unit_price":"10.00"}]}`, newOperationKey(), newOperationKey(), j.ID, c.ID)
	created := call("POST", "/invoices", body, 200)
	if replay := call("POST", "/invoices", body, 200); replay != created {
		t.Fatal("new exact numeric intent did not retain its receipt")
	}
	call("POST", "/invoices", strings.Replace(body, "1.50000000000000001", "1.50000000000000002", 1), 409)
	empty, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Lost commercial access", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, actor.ID, empty); err != nil {
		t.Fatal(err)
	}
	for _, old := range originals {
		call("GET", fmt.Sprintf("/invoices/%d", old.id), "", 404)
		replay := call("POST", "/invoices", old.body, 200)
		for _, private := range []string{"line_items", "unit_price", "Legacy quantity", "financial"} {
			assertNotContains(t, replay, private)
		}
		call("GET", "/commercial-operations/"+old.operation, "", 200)
		var preserved bool
		if err := pool.QueryRow(ctx, `SELECT receipt=$2::jsonb AND (SELECT financial='' FROM invoices WHERE id=$3) FROM commercial_operations WHERE operation_id=$1`, old.operation, old.receipt, old.id).Scan(&preserved); err != nil {
			t.Fatal(err)
		}
		if !preserved {
			t.Fatal("legacy receipt or document arithmetic was rewritten")
		}
	}
}
