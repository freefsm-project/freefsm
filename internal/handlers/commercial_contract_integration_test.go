package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

// A raw request intentionally omits the form's concurrency controls. Knowing a
// document identity must never permit an unconditional commercial overwrite.
func TestCommercialRequiresReviewedRevisionHTTP(t *testing.T) {
	for _, kind := range []string{"estimate", "invoice"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			cookie := sessionCookie(t, ctx, sessions, admin)
			customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Commercial revision customer").SaveX(ctx)
			values := url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Reviewed title"}, "invoice_date": {"2026-10-13"}, "due_date": {""}, "line_items": {"[]"}, "tax_rate": {"0"}}
			documentTransactionRequest(t, router, cookie, "POST", "/"+kind+"s", values, 303)
			var id int64
			if err := pool.QueryRow(ctx, `SELECT id FROM `+kind+`s`).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if kind == "invoice" {
				var number int64
				if err := pool.QueryRow(ctx, `SELECT invoice_number FROM invoices WHERE id=$1`, id).Scan(&number); err != nil {
					t.Fatal(err)
				}
				values.Set("invoice_number", fmt.Sprint(number))
			}
			values.Set("title", "Unreviewed overwrite")
			w := jobRepairRawPost(router, cookie, fmt.Sprintf("/%ss/%d", kind, id), values)
			if w.Code != 428 {
				t.Fatalf("unguarded commercial update = %d, want 428: %s", w.Code, w.Body.String())
			}
			var title string
			if err := pool.QueryRow(ctx, `SELECT title FROM `+kind+`s WHERE id=$1`, id).Scan(&title); err != nil {
				t.Fatal(err)
			}
			if title != "Reviewed title" {
				t.Fatalf("rejected update persisted %q", title)
			}
		})
	}
}

func TestCommercialCreateOnceAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Field customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Field job").SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Commercial acceptance tablet")
	if err != nil {
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
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		return w.Body.String()
	}
	for _, kind := range []string{"estimate", "invoice"} {
		operation, identity := newOperationKey(), newOperationKey()
		body := fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"Field document","line_items":[]}`, operation, identity, j.ID)
		first := call("POST", "/"+kind+"s", body, 200)
		replay := call("POST", "/"+kind+"s", body, 200)
		if first != replay {
			t.Fatalf("create replay changed effect: %s / %s", first, replay)
		}
		var effect struct {
			Data struct {
				ID       int64  `json:"id"`
				Revision string `json:"revision"`
				Number   string `json:"number"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(first), &effect); err != nil {
			t.Fatal(err)
		}
		if effect.Data.ID == 0 || effect.Data.Revision == "" || effect.Data.Number == "" {
			t.Fatalf("missing server identity/revision: %s", first)
		}
		call("POST", "/"+kind+"s", strings.Replace(body, "Field document", "Changed intent", 1), 409)
		call("POST", "/"+kind+"s", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":0,"title":"No fallback"}`, newOperationKey(), newOperationKey()), 422)
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+kind+`s`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("create-once persisted %d %ss", count, kind)
		}
	}
}

func TestCommercialInvoiceClearsOptionalContentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Optional values customer").SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Reviewed"}, "notes": {"Remove this note"}, "invoice_date": {"2026-10-13"}, "due_date": {"2026-11-13"}, "line_items": {"[]"}, "tax_rate": {"0"}}
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", values, 303)
	var id, number int64
	if err := pool.QueryRow(ctx, `SELECT id,invoice_number FROM invoices`).Scan(&id, &number); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d", id)
	values.Set("invoice_number", fmt.Sprint(number))
	values.Set("due_date", "")
	values.Set("notes", "")
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	var empty bool
	if err := pool.QueryRow(ctx, `SELECT notes='' AND due_date='0001-01-01'::date FROM invoices WHERE id=$1`, id).Scan(&empty); err != nil {
		t.Fatal(err)
	}
	if !empty {
		t.Fatal("explicit empty notes / No Due Date did not persist")
	}
	var changed bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE action='invoice.edit' AND target_id=$1 AND detail->'before'->>'notes'='Remove this note' AND detail->'after'->>'notes'='')`, id).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("commercial before/after activity missing")
	}
	history := documentTransactionRequest(t, router, cookie, "GET", path+"/commercial-history", nil, 200).Body.String()
	assertContains(t, history, "Remove this note")
}

func commercialReviewedValues(t *testing.T, router http.Handler, cookie *http.Cookie, path string, values url.Values) url.Values {
	t.Helper()
	body := documentTransactionRequest(t, router, cookie, "GET", path+"/edit", nil, 200).Body.String()
	match := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("missing commercial revision")
	}
	values.Set("save_action_id", newOperationKey())
	values.Set("save_revision", match[1])
	return values
}

func TestCommercialAssignedEditCannotMoveAnchorsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	adminCookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Assigned customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Original job").SaveX(ctx)
	j2 := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Other assigned job").SaveX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Content author").SetEmail("content-author@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	var grants []authorization.Grant
	for _, permission := range []string{"estimate.view", "estimate.edit", "job.view", "pricing.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.Assigned})
	}
	grants = append(grants, authorization.Grant{Permission: "customer.view", Scope: authorization.Context})
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Content only", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$3),($2,$3)`, j.ID, j2.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Assigned document"}, "line_items": {"[]"}, "tax_rate": {"0"}}
	documentTransactionRequest(t, router, adminCookie, "POST", "/estimates", values, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, ctx, sessions, u.ID)
	path := fmt.Sprintf("/estimates/%d", id)
	values = commercialReviewedValues(t, router, cookie, path, values)
	values.Set("job_id", fmt.Sprint(j2.ID))
	documentTransactionRequest(t, router, cookie, "POST", path, values, 403)
}

func TestCommercialJobCustomerReassignmentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original customer").SaveX(ctx)
	c2 := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reviewed replacement").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reassigned work").SaveX(ctx)
	for _, kind := range []string{"estimate", "invoice"} {
		values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Preserved document"}, "invoice_date": {"2026-10-13"}, "line_items": {"[]"}, "tax_rate": {"0"}}
		documentTransactionRequest(t, router, cookie, "POST", "/"+kind+"s", values, 303)
	}
	path := fmt.Sprintf("/jobs/%d", j.ID)
	values := url.Values{"form_mode": {"edit"}, "job_id": {fmt.Sprint(j.ID)}, "customer_id": {fmt.Sprint(c2.ID)}, "job_type": {"Reassigned work"}, "replace_customer": {"true"}}
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	for _, table := range []string{"jobs", "estimates", "invoices"} {
		var got int64
		if err := pool.QueryRow(ctx, `SELECT customer_id FROM `+table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != c2.ID {
			t.Fatalf("%s retained incompatible Customer %d", table, got)
		}
	}
}

func TestCommercialABADriftHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("ABA customer").SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Original"}, "line_items": {"[]"}, "tax_rate": {"0"}}
	documentTransactionRequest(t, router, cookie, "POST", "/estimates", values, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/estimates/%d", id)
	body := documentTransactionRequest(t, router, cookie, "GET", path+"/edit", nil, 200).Body.String()
	match := regexp.MustCompile(`name="save_revision" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatal("missing commercial revision")
	}
	// Legacy/raw writers do not maintain updated_at: a monotonic commercial
	// revision must still detect the intervening change after values return to A.
	if _, err := pool.Exec(ctx, `UPDATE estimates SET notes='Intervening B' WHERE id=$1;`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE estimates SET notes='' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	values.Set("save_action_id", newOperationKey())
	values.Set("save_revision", match[1])
	values.Set("title", "Stale intent")
	documentTransactionRequest(t, router, cookie, "POST", path, values, 409)
}

func TestCommercialBroadCustomerChangeReviewHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Original").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Replacement").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Original anchor").SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Reviewed document"}, "notes": {"Keep explicit notes"}, "line_items": {"[]"}, "tax_rate": {"0"}}
	documentTransactionRequest(t, router, cookie, "POST", "/estimates", values, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/estimates/%d", id)
	values = commercialReviewedValues(t, router, cookie, path, values)
	values.Set("customer_id", fmt.Sprint(next.ID))
	documentTransactionRequest(t, router, cookie, "POST", path, values, 409)
	values = commercialReviewedValues(t, router, cookie, path, values)
	values.Set("customer_defaults_reviewed", "true")
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	var correct bool
	if err := pool.QueryRow(ctx, `SELECT customer_id=$2 AND job_id IS NULL AND notes='Keep explicit notes' FROM estimates WHERE id=$1`, id, next.ID).Scan(&correct); err != nil {
		t.Fatal(err)
	}
	if !correct {
		t.Fatal("reviewed Customer change did not retain content and clear incompatible anchor")
	}
}

func TestCommercialFinalizeRequiresReviewedRevisionHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Finalize customer").SaveX(ctx)
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Zero Invoice"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	w := jobRepairRawPost(router, cookie, fmt.Sprintf("/invoices/%d/finalize", id), nil)
	if w.Code != 428 {
		t.Fatalf("unguarded Finalize=%d want 428: %s", w.Code, w.Body.String())
	}
}

func TestCommercialCurrentlyValidDiscoveryDoesNotChangeStatusHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Validity Customer").SaveX(ctx)
	for _, value := range []struct{ title, date string }{{"Expired commercial proposal", "2001-01-01"}, {"Undated commercial proposal", ""}, {"Future commercial proposal", time.Now().AddDate(1, 0, 0).Format("2006-01-02")}} {
		documentTransactionRequest(t, router, cookie, "POST", "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {value.title}, "valid_through": {value.date}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	}
	all := documentTransactionRequest(t, router, cookie, "GET", "/estimates", nil, 200).Body.String()
	assertContains(t, all, "Expired commercial proposal")
	current := documentTransactionRequest(t, router, cookie, "GET", "/estimates?valid=current", nil, 200).Body.String()
	assertNotContains(t, current, "Expired commercial proposal")
	assertContains(t, current, "Undated commercial proposal")
	assertContains(t, current, "Future commercial proposal")
	var draft bool
	if err := pool.QueryRow(ctx, `SELECT bool_and(s.category_key='estimate:draft') FROM estimates e JOIN statuses s ON s.id=e.status_id`).Scan(&draft); err != nil {
		t.Fatal(err)
	}
	if !draft {
		t.Fatal("validity discovery changed manual workflow")
	}
}

func TestCommercialCustomerReviewUsesEditNotCreateGrantHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Existing Customer").SaveX(ctx)
	next := client.Customer.Create().SetCompanyID(company).SetDisplayName("Eligible replacement").SaveX(ctx)
	if _, err := pool.Exec(ctx, `INSERT INTO customer_document_defaults(customer_id,defaults) VALUES($1,'{"estimate_terms":"Replacement Customer terms"}')`, next.ID); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, router, cookie, "POST", "/estimates", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Existing Estimate"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	e := client.Estimate.Query().OnlyX(ctx)
	u := client.User.Create().SetCompanyID(company).SetName("Editor only").SetEmail("commercial-editor-only@test").SetPasswordHash("x").SetRole("tech").SaveX(ctx)
	auth := authorization.New(pool)
	var grants []authorization.Grant
	for _, permission := range []string{"estimate.view", "estimate.edit", "customer.view", "pricing.view"} {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Commercial edit without Create", grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	form := documentTransactionRequest(t, router, sessionCookie(t, ctx, sessions, u.ID), "GET", fmt.Sprintf("/estimates/%d/edit", e.ID), nil, 200).Body.String()
	assertContains(t, form, "Replacement Customer terms")
}

func TestCommercialHistoryUsesParentAndPricingGrantsHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("History Customer").SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"History Invoice"}, "notes": {"Before public note"}, "line_items": {`[{"title":"Captured line","quantity":1,"unit_price":5}]`}, "tax_rate": {"0"}}
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", values, 303)
	i := client.Invoice.Query().OnlyX(ctx)
	path := fmt.Sprintf("/invoices/%d", i.ID)
	values.Set("invoice_number", fmt.Sprint(i.InvoiceNumber))
	values.Set("line_items", `[{"title":"Captured line","quantity":1,"unit_price":99999.75}]`)
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	values.Set("notes", "After public note")
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
	u := client.User.Create().SetCompanyID(company).SetName("Basic reader").SetEmail("commercial-history@test").SetPasswordHash("x").SetRole("admin").SaveX(ctx)
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, 0, "Basic document reader", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}})
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, authorization.Actor{UserID: admin}, u.ID, role); err != nil {
		t.Fatal(err)
	}
	reader := sessionCookie(t, ctx, sessions, u.ID)
	history := documentTransactionRequest(t, router, reader, "GET", path+"/commercial-history", nil, 200).Body.String()
	assertContains(t, history, "Before public note")
	assertContains(t, history, "After public note")
	assertNotContains(t, history, "99999")
	assertNotContains(t, history, "Captured line")
	adminHistory := documentTransactionRequest(t, router, cookie, "GET", path+"/commercial-history", nil, 200).Body.String()
	assertContains(t, adminHistory, "99999.75")
	if _, err := auth.SaveRole(ctx, authorization.Actor{UserID: admin}, role, "Reader without document access", nil, 1); err != nil {
		t.Fatal(err)
	}
	documentTransactionRequest(t, router, reader, "GET", path+"/commercial-history", nil, 404)
}
