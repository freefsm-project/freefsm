package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCommercialReceiptAfterScopeLossAndAtomicAuditFailureAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Sensitive Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Sensitive Job").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("API author").SetEmail("api-commercial@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	var grants []authorization.Grant
	for _, permission := range []string{"job.view", "customer.view", "pricing.view", "estimate.view", "estimate.create", "estimate.edit"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "API commercial author", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, u.ID, "Offline commercial device")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string, want int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s =%d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	operation := newOperationKey()
	body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"SECRET commercial title","notes":"SECRET terms","line_items":[]}`, operation, newOperationKey(), j.ID, c.ID)
	if _, err := pool.Exec(ctx, `CREATE FUNCTION commercial_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.target_type='estimate' THEN RAISE EXCEPTION 'injected commercial audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER commercial_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION commercial_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	call("POST", "/estimates", body, 503)
	var effects int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM estimates)+(SELECT count(*) FROM commercial_operations)+(SELECT count(*) FROM commercial_client_identities)+(SELECT count(*) FROM issued_document_numbers WHERE document_type='estimate')`).Scan(&effects); err != nil {
		t.Fatal(err)
	}
	if effects != 0 {
		t.Fatalf("audit failure escaped %d durable effects", effects)
	}
	call("GET", "/commercial-operations/"+operation, "", 404)
	if _, err := pool.Exec(ctx, `DROP TRIGGER commercial_audit_failure ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	accepted := call("POST", "/estimates", body, 200)
	var result struct {
		Data authorization.CommercialReceipt `json:"data"`
	}
	if err := json.Unmarshal([]byte(accepted), &result); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "No commercial visibility", nil, 1); err != nil {
		t.Fatal(err)
	}
	if got := call("GET", "/commercial-operations/"+operation, "", 200); got != accepted {
		t.Fatalf("lost-scope receipt changed: %s / %s", got, accepted)
	}
	if got := call("POST", "/estimates", body, 200); got != accepted || strings.Contains(got, "SECRET") {
		t.Fatal("replay leaked content or changed receipt after scope loss")
	}
	call("GET", fmt.Sprintf("/estimates/%d", result.Data.ID), "", 404)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM estimates`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("receipt replay persisted %d docs: %v", count, err)
	}
}

func TestCommercialAPIDefaultsAreCreationSnapshots(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Default Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Default Job").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET default_invoice_notes='Company note',invoice_payment_terms='Company terms',default_line_description='Company description',default_due_days=30 WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customer_document_defaults(customer_id,defaults) VALUES($1,'{"invoice_notes":"Customer note","invoice_terms":"Customer terms","line_description":"Customer description","invoice_due_days":"12"}')`, c.ID); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Defaults tablet")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	for _, description := range []string{"", `,"description":""`} {
		body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Defaults","notes":"","due_date":"","line_items":[{"title":"Snapshot","quantity":1,"unit_price":"17.25"%s}]}`, newOperationKey(), newOperationKey(), j.ID, c.ID, description)
		r := httptest.NewRequest("POST", "/invoices", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("default creation=%d: %s", w.Code, w.Body.String())
		}
		var out struct {
			Data authorization.CommercialReceipt `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		var note, terms, captured string
		var noDue bool
		if err := pool.QueryRow(ctx, `SELECT notes,terms,line_items->0->>'description',due_date='0001-01-01'::date FROM invoices WHERE id=$1`, out.Data.ID).Scan(&note, &terms, &captured, &noDue); err != nil {
			t.Fatal(err)
		}
		want := "Customer description"
		if description != "" {
			want = ""
		}
		if note != "" || terms != "Customer terms" || captured != want || !noDue {
			t.Fatalf("defaults note=%q terms=%q description=%q noDue=%v", note, terms, captured, noDue)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE customer_document_defaults SET defaults='{"invoice_terms":"Future terms"}' WHERE customer_id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	var unchanged bool
	if err := pool.QueryRow(ctx, `SELECT bool_and(terms='Customer terms') FROM invoices`).Scan(&unchanged); err != nil {
		t.Fatal(err)
	}
	if !unchanged {
		t.Fatal("defaults rewrote existing document")
	}
}

func TestCommercialAllRecordsContentDoesNotRequireJobViewAPI(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retained anchor Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Retained Job anchor").SaveX(ctx)
	documentTransactionRequest(t, router, sessionCookie(t, ctx, sessions, admin), "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Independent Invoice"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	i := client.Invoice.Query().OnlyX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Independent editor").SetEmail("independent-commercial@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Invoice-only editor", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.edit", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, u.ID, "Independent content editor")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, fmt.Sprintf("/invoices/%d", i.ID), strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		return w
	}
	for _, archived := range []bool{false, true} {
		if archived {
			client.Job.UpdateOneID(j.ID).SetDeletedAt(time.Now()).SaveX(ctx)
		}
		w := call("GET", "")
		if w.Code != 200 {
			t.Fatalf("independent View (archived Job=%v)=%d: %s", archived, w.Code, w.Body.String())
		}
		assertNotContains(t, w.Body.String(), "line_items")
		var state struct {
			Data struct {
				Revision string `json:"revision"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		w = call("PATCH", fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"notes":"Independent content edit"}`, newOperationKey(), j.ID, state.Data.Revision))
		if w.Code != 200 {
			t.Fatalf("independent Edit (archived Job=%v)=%d: %s", archived, w.Code, w.Body.String())
		}
	}
}

func TestCommercialSingleConnectionTransactionAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Single connection Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Single connection Job").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, admin); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, admin, "Bound transaction")
	if err != nil {
		t.Fatal(err)
	}
	config := pool.Config()
	config.MaxConns = 1
	limited, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	api := apiv1.NewRouter(limited, client, sessions)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"One connection","line_items":[]}`, newOperationKey(), newOperationKey(), j.ID, c.ID)
	r := httptest.NewRequest("POST", "/estimates", strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("transaction tried to escape its only connection: %d %s", w.Code, w.Body.String())
	}
}

func TestCommercialAPISelectedItemAuditSharesOperationIdentity(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Selection Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Selection Job").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, j.ID, admin); err != nil {
		t.Fatal(err)
	}
	item := client.Item.Create().SetCompanyID(company).SetName("Selected Item").SetUnitPrice(22).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Selection tablet")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	operation := newOperationKey()
	body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Selection","line_items":[{"item_id":%d,"title":"Reviewed snapshot","quantity":1,"unit_price":"17.25"}]}`, operation, newOperationKey(), j.ID, c.ID, item.ID)
	r := httptest.NewRequest("POST", "/estimates", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("selection=%d: %s", w.Code, w.Body.String())
	}
	var attributed bool
	if err := pool.QueryRow(ctx, `SELECT count(*)=1 AND bool_and(operation_id=$2::uuid AND occurred_at IS NOT NULL AND session_id IS NOT NULL AND device_name='Selection tablet') FROM authorization_audit WHERE target_type='item' AND target_id=$1 AND action='pricebook.view' AND channel='api'`, item.ID, operation).Scan(&attributed); err != nil {
		t.Fatal(err)
	}
	if !attributed {
		t.Fatal("selected Item audit lost API operation/device/occurrence attribution")
	}
}
