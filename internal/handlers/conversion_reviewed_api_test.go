package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func conversionAPICall(t *testing.T, api http.Handler, token, method, path string, in any, status int) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(in)
	r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s=%d want%d: %s", method, path, w.Code, status, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if status >= 400 {
		return out
	}
	return out["data"].(map[string]any)
}

func TestReviewedConversionPreviewPairingAndAtomicFailureAPI(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Pair preview").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Pair preview").SaveX(ctx)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Source label"}, "object_type": {"estimate"}, "field_type": {"text"}, "conversion_key": {"explicit"}}, 303)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Different target label"}, "object_type": {"invoice"}, "field_type": {"text"}, "conversion_key": {"explicit"}}, 303)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Unpaired default"}, "object_type": {"invoice"}, "field_type": {"text"}, "default_value": {"Default value"}}, 303)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Required input"}, "object_type": {"invoice"}, "field_type": {"text"}, "required": {"true"}}, 303)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Preview")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, in any, status int) map[string]any {
		return conversionAPICall(t, api, token, method, path, in, status)
	}
	created := call("POST", "/estimates", map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "title": "Preview source", "custom_fields": []any{map[string]any{"definition_id": 1, "value": "Source wins"}}}, 200)
	path := fmt.Sprintf("/estimates/%.0f", created["id"])
	preview := call("GET", path+"/conversion-preview", nil, 200)
	if preview["invoice_date"] == nil {
		t.Fatal("preview omitted current Invoice date defaults", preview)
	}
	if !strings.Contains(fmt.Sprint(preview["validation"]), "Required input") {
		t.Fatal(preview)
	}
	fields := []any{map[string]any{"definition_id": 2, "value": "Must not win"}, map[string]any{"definition_id": 4, "value": "Explicit required"}}
	preview = call("POST", path+"/conversion-preview", map[string]any{"custom_fields": fields}, 200)
	if strings.Contains(fmt.Sprint(preview["invoice_fields"]), "Must not win") || !strings.Contains(fmt.Sprint(preview["invoice_fields"]), "Source wins") || !strings.Contains(fmt.Sprint(preview["invoice_fields"]), "Default value") {
		t.Fatal(preview)
	}
	bad := call("POST", path+"/conversion-preview", map[string]any{"custom_fields": []any{map[string]any{"definition_id": 999, "value": "invalid"}}}, 200)
	if bad["validation"] == nil {
		t.Fatal(bad)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM estimate_invoice_conversion_cycles)+(SELECT count(*) FROM invoices)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("preview effects=%d %v", count, err)
	}
	in := map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": preview["revision"], "custom_fields": fields}
	invalid := map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": preview["revision"], "custom_fields": []any{map[string]any{"definition_id": 999, "value": "invalid"}}}
	call("POST", path+"/convert", invalid, 422)
	if receipt := call("GET", "/commercial-operations/"+invalid["operation_id"].(string), nil, 200); receipt["outcome"] != "invalid_document" {
		t.Fatal(receipt)
	}
	invalid["custom_fields"] = fields
	call("POST", path+"/convert", invalid, 409) // changed intent cannot reuse rejected identity
	if _, err := pool.Exec(ctx, `CREATE FUNCTION conversion_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='estimate.convert' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='injected conversion audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER conversion_audit_failure BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION conversion_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	call("POST", path+"/convert", in, 503)
	call("GET", "/commercial-operations/"+in["operation_id"].(string), nil, 404)
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM estimate_invoice_conversion_cycles)+(SELECT count(*) FROM invoices)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("audit failure effects=%d %v", count, err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER conversion_audit_failure ON authorization_audit; DROP FUNCTION conversion_audit_failure()`); err != nil {
		t.Fatal(err)
	}
	result := call("POST", path+"/convert", in, 200)
	invoice := call("GET", fmt.Sprintf("/invoices/%.0f", result["id"]), nil, 200)
	if fmt.Sprint(invoice["custom_fields"]) != fmt.Sprint(preview["invoice_fields"]) {
		t.Fatal("preview/commit diverged", preview, invoice)
	}
}

func TestReviewedConversionRequiresIndependentInvoiceEditAPI(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Independent conversion").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Independent conversion").SaveX(ctx)
	fieldPost(t, web, cookie, "/estimates", url.Values{"title": {"Independent content"}, "customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "line_items": {"[]"}}, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Convert without Invoice Edit", []authorization.Grant{
		{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.convert", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.create", Scope: authorization.All},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := client.User.Create().SetCompanyID(company).SetEmail("independent-convert@test").SetName("Converter").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	token, err := sessions.CreateMobile(ctx, u.ID, "Independent conversion")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path string, in any, status int) map[string]any {
		return conversionAPICall(t, api, token, method, path, in, status)
	}
	path := fmt.Sprintf("/estimates/%d", id)
	preview := call("GET", path+"/conversion-preview", nil, 200)
	if preview["can_edit"] != false || preview["line_items"] != nil {
		t.Fatal("preview leaked independent pricing or edit authority", preview)
	}
	call("POST", path+"/conversion-preview", map[string]any{"custom_fields": []any{}}, 403)
	in := map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": preview["revision"], "custom_fields": []any{}}
	call("POST", path+"/convert", in, 403)
	delete(in, "custom_fields")
	in["operation_id"] = newOperationKey()
	result := call("POST", path+"/convert", in, 200)
	if result["document_type"] != "invoice" {
		t.Fatal(result)
	}
	// Revoke the Role's authority by explicit reassignment. The original receipt
	// stays queryable but neither content reads nor a fresh conversion is granted.
	empty, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "No conversion", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, empty); err != nil {
		t.Fatal(err)
	}
	call("GET", "/commercial-operations/"+in["operation_id"].(string), nil, 200)
	call("POST", path+"/convert", in, 200)
	in["operation_id"] = newOperationKey()
	call("POST", path+"/convert", in, 403)
}

func TestReviewedConversionCycleAndContentDriftAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reviewed conversion").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reviewed conversion").SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Conversion")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, in map[string]any, status int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(in)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if status >= 400 {
			return out
		}
		return out["data"].(map[string]any)
	}
	created := call("POST", "/estimates", map[string]any{"operation_id": newOperationKey(), "client_id": newOperationKey(), "job_id": j.ID, "title": "Original", "line_items": []any{}}, 200)
	e := fmt.Sprintf("/estimates/%.0f", created["id"])
	intent := func(state map[string]any) map[string]any {
		return map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": state["revision"]}
	}
	stale := intent(created)
	edit := intent(created)
	edit["title"] = "Reviewed current content"
	current := call("PATCH", e, edit, 200)
	failed := call("POST", e+"/convert", stale, 409)
	if !strings.Contains(fmt.Sprint(failed), "stale_commercial_revision") {
		t.Fatal(failed)
	}
	receipt := call("GET", "/commercial-operations/"+stale["operation_id"].(string), nil, 200)
	if receipt["outcome"] != "stale_commercial_revision" {
		t.Fatal(receipt)
	}
	convert := intent(current)
	first := call("POST", e+"/convert", convert, 200)
	replay := call("POST", e+"/convert", convert, 200)
	if fmt.Sprint(first) != fmt.Sprint(replay) {
		t.Fatal("replay changed receipt", first, replay)
	}
	i := fmt.Sprintf("/invoices/%.0f", first["id"])
	state := call("GET", i, nil, 200)
	revert := intent(state)
	revert["cycle_id"] = first["cycle_id"]
	back := call("POST", i+"/revert-to-estimate", revert, 200)
	second := call("POST", e+"/convert", intent(back), 200)
	if first["id"] == second["id"] || first["cycle_id"] == second["cycle_id"] || first["number"] == second["number"] {
		t.Fatal("reused conversion identity", first, second)
	}
	secondPath := fmt.Sprintf("/invoices/%.0f", second["id"])
	wrongCycle := intent(call("GET", secondPath, nil, 200))
	wrongCycle["cycle_id"] = first["cycle_id"]
	failed = call("POST", secondPath+"/revert-to-estimate", wrongCycle, 409)
	if !strings.Contains(fmt.Sprint(failed), "stale_conversion_cycle") {
		t.Fatal(failed)
	}
	call("POST", i+"/revert-to-estimate", revert, 200) // original receipt, never acts on the new cycle
	oldCycle := map[string]any{"operation_id": newOperationKey(), "job_id": j.ID, "revision": state["revision"], "cycle_id": first["cycle_id"]}
	failed = call("POST", i+"/revert-to-estimate", oldCycle, 409)
	if !strings.Contains(fmt.Sprint(failed), "stale_conversion_cycle") {
		t.Fatal(failed)
	}
	call("GET", secondPath, nil, 200)
	var active int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM estimate_invoice_conversion_cycles WHERE reverted_at IS NULL`).Scan(&active); err != nil || active != 1 {
		t.Fatalf("active cycles=%d %v", active, err)
	}
}
