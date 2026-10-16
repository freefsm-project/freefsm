package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func TestCommercialLifecycleVoidReversibleHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Lifecycle Customer").SaveX(ctx)
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Lifecycle Invoice"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d", id)
	documentTransactionRequest(t, router, cookie, "POST", path+"/finalize", commercialReviewedValues(t, router, cookie, path, url.Values{}), 303)
	for _, category := range []string{"invoice:void", "invoice:sent", "invoice:void", "invoice:invoiced", "invoice:draft"} {
		var status int64
		if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2`, company, category).Scan(&status); err != nil {
			t.Fatal(err)
		}
		values := commercialReviewedValues(t, router, cookie, path, url.Values{"status_id": {fmt.Sprint(status)}})
		documentTransactionRequest(t, router, cookie, "POST", path+"/status", values, 303)
	}
}

func TestCommercialLifecycleVoidPDFPreservesPriorBytesHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Void PDF Customer").SaveX(ctx)
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Void PDF Invoice"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	var id, void int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:void'`, company).Scan(&void); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d", id)
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
	original := requestBody(t, router, cookie, "GET", path+"/pdf", 200)
	var version int64
	if err := pool.QueryRow(ctx, `SELECT id FROM document_pdf_versions`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	svc := statusflow.New(pool)
	actor := statusflow.Actor{ID: admin, CompanyID: company}
	if err := svc.FinalizeInvoice(ctx, actor, id); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(ctx, actor, void, "Withdrawn", "#EF4444"); err != nil {
		t.Fatal(err)
	}
	if err := svc.TransitionInvoice(ctx, actor, id, void); err != nil {
		t.Fatal(err)
	}
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
	pdf := requestBody(t, router, cookie, "GET", path+"/pdf", 200)
	command := exec.Command("pdftotext", "-", "-")
	command.Stdin = strings.NewReader(pdf)
	text, err := command.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, string(text), "VOID")
	if old := requestBody(t, router, cookie, "GET", fmt.Sprintf("%s/pdf?version=%d&confirm_stale=1", path, version), 200); old != original {
		t.Fatal("Void mutated prior PDF bytes")
	}
}

func TestCommercialLifecycleFinalizeAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API lifecycle Customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("API lifecycle Job").SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Lifecycle device")
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
	created := call("POST", "/invoices", fmt.Sprintf(`{"operation_id":%q,"client_id":%q,"job_id":%d,"title":"API lifecycle Invoice","line_items":[]}`, newOperationKey(), newOperationKey(), j.ID), 200)
	var receipt struct {
		Data struct {
			ID       int64  `json:"id"`
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(created), &receipt); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d", receipt.Data.ID)
	var target int64
	var meaning string
	if err := pool.QueryRow(ctx, `SELECT id,meaning_revision::text FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced'`, company).Scan(&target, &meaning); err != nil {
		t.Fatal(err)
	}
	intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"revision":%q,"status_id":%d,"meaning_revision":%q}`, newOperationKey(), j.ID, receipt.Data.Revision, target, meaning)
	first := call("POST", path+"/finalize", intent, 200)
	if replay := call("POST", path+"/finalize", intent, 200); replay != first {
		t.Fatalf("replay changed receipt: %s / %s", first, replay)
	}
	state := call("GET", path, "", 200)
	assertContains(t, state, `"effective_category":"invoice:paid"`)
	assertContains(t, state, `"manual_category":"invoice:invoiced"`)
}

func TestCommercialLifecycleDraftRequiresFinalizeHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Finalize Customer").SaveX(ctx)
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Unissued Invoice"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d", id)
	for _, category := range []string{"invoice:invoiced", "invoice:sent", "invoice:void"} {
		var status int64
		if err := pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key=$2`, company, category).Scan(&status); err != nil {
			t.Fatal(err)
		}
		values := commercialReviewedValues(t, router, cookie, path, url.Values{"status_id": {fmt.Sprint(status)}})
		documentTransactionRequest(t, router, cookie, "POST", path+"/status", values, 409)
	}
	documentTransactionRequest(t, router, cookie, "POST", path+"/finalize", commercialReviewedValues(t, router, cookie, path, url.Values{}), 303)
	body := documentTransactionRequest(t, router, cookie, "GET", path, nil, 200).Body.String()
	assertContains(t, body, "Paid")
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM invoice_payments`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("zero-total Finalize fabricated %d Payments", count)
	}
}
