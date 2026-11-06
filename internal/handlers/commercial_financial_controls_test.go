package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommercialFinancialAPIControls(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Financial control Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Financial control Job").SaveX(ctx)
	assignFieldDocumentCreator(t, pool, j.ID, admin)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Financial controls")
	if err != nil {
		t.Fatal(err)
	}
	call := func(token, method, path, body string, want int) string {
		t.Helper()
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
	create := func(lines string) string {
		return fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"Financial control","tax_rate":"10","line_items":%s}`, newOperationKey(), newOperationKey(), j.ID, c.ID, lines)
	}
	for _, lines := range []string{`[{"title":"Invalid","quantity":"0","unit_price":"1.00"}]`, `[{"title":"Negative","quantity":"1","unit_price":"-1.00"}]`, `[{"title":"Negative adjusted","quantity":"0.001","unit_price":"-0.01","discount_adjustment":{"kind":"fixed","value":"0.00"}}]`, `[{"title":"Untaxed","quantity":"1","unit_price":"0.10"},{"title":"Taxable correction","quantity":"1","unit_price":"-0.10","taxable":true}]`} {
		call(token, "POST", "/invoices", create(lines), 422)
	}
	call(token, "POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"customer_id":%d,"title":"No allocation targets","adjustments":{"surcharge":{"kind":"fixed","value":"0.01"}},"line_items":[]}`, newOperationKey(), newOperationKey(), j.ID, c.ID), 422)
	var untouched bool
	if err := pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM invoices WHERE company_id=$1) AND NOT EXISTS(SELECT 1 FROM issued_document_numbers WHERE company_id=$1 AND document_type='invoice') AND NOT EXISTS(SELECT 1 FROM commercial_client_identities WHERE user_id=$2 AND document_type='invoice')`, company, admin).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if !untouched {
		t.Fatal("invalid monetary commands published partial documents, numbers or identities")
	}
	body := create(`[{"title":"CONFIDENTIAL EXACT AMOUNT","quantity":"1","unit_price":"10.00","taxable":true}]`)
	raw := call(token, "POST", "/invoices", body, 200)
	var effect struct {
		Data struct {
			ID       int64  `json:"id"`
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(raw), &effect); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d", effect.Data.ID)
	adjustment := func(revision string) string {
		return fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"adjustments":{"discount":{"kind":"percentage","value":"50"}}}`, newOperationKey(), j.ID, revision)
	}
	good := adjustment(effect.Data.Revision)
	call(token, "PATCH", path, good, 200)
	call(token, "PATCH", path, adjustment(effect.Data.Revision), 409)
	state := call(token, "GET", path, "", 200)
	assertContains(t, state, `"total_cents":"550"`)
	var read struct {
		Data struct {
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(state), &read); err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetName("No captured pricing").SetEmail("no-captured-pricing@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Commercial metadata only", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.edit", Scope: authorization.All}, {Permission: "job.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	limited, err := sessions.CreateMobile(ctx, u.ID, "No pricing")
	if err != nil {
		t.Fatal(err)
	}
	denied := call(limited, "GET", path, "", 200)
	for _, secret := range []string{`"financial"`, "CONFIDENTIAL EXACT AMOUNT", `"totals"`} {
		assertNotContains(t, denied, secret)
	}
	// A changed discount requires independent captured-pricing authority even
	// when the caller supplies no line_items or tax_rate field.
	var limitedRead struct {
		Data struct {
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(denied), &limitedRead); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(adjustment(limitedRead.Data.Revision), `"50"`, `"25"`, 1)
	call(limited, "PATCH", path, changed, 403)
	assertContains(t, call(token, "GET", path, "", 200), `"total_cents":"550"`)
	// Dependency failures remain retryable with the original operation identity.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION t48_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit dependency'; END $$; CREATE TRIGGER t48_reject_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION t48_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	retry := strings.Replace(adjustment(read.Data.Revision), `"50"`, `"25"`, 1)
	call(token, "PATCH", path, retry, 503)
	var operation struct {
		ID string `json:"operation_id"`
	}
	if err := json.Unmarshal([]byte(retry), &operation); err != nil {
		t.Fatal(err)
	}
	call(token, "GET", "/commercial-operations/"+operation.ID, "", 404)
	if _, err := pool.Exec(ctx, `DROP TRIGGER t48_reject_audit ON authorization_audit; DROP FUNCTION t48_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	accepted := call(token, "PATCH", path, retry, 200)
	if replay := call(token, "PATCH", path, retry, 200); replay != accepted {
		t.Fatal("same-ID retry changed its immutable receipt")
	}
	assertContains(t, call(token, "GET", path, "", 200), `"total_cents":"825"`)
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE action='invoice.edit' AND detail->>'operation_id'=$1`, operation.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("replayed monetary intent produced %d success audits", audits)
	}
}
