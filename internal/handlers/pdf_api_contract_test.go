package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestPDFReviewedSourceReceiptAndDependentDriftAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("API PDF customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("API PDF Job").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Reviewed invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "PDF reference")
	historyCheck(t, err)
	call := func(method, path, body string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.String())
		}
		var out map[string]any
		historyCheck(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	path := fmt.Sprintf("/invoices/%d/pdf", i.ID)
	reviewed := call("GET", path+"/source", "", 200)["data"].(map[string]any)
	preview := reviewed["source"].(map[string]any)
	if preview["document"].(map[string]any)["title"] != "Reviewed invoice" || preview["customer"].(map[string]any)["display_name"] != "API PDF customer" {
		t.Fatalf("review omitted captured content: %v", preview)
	}
	if _, leaked := preview["allocations"]; leaked {
		t.Fatal("review leaked settlement ledger")
	}
	review := reviewed["source_token"].(string)
	key := newOperationKey()
	intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, key, j.ID, review)
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_pdf_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='invoice.pdf.generate' THEN RAISE EXCEPTION USING ERRCODE='23514',MESSAGE='injected PDF audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_pdf_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_pdf_audit()`)
	historyCheck(t, err)
	call("POST", path+"/generate", intent, 503)
	call("GET", "/pdf-operations/"+key, "", 404)
	var failedVersions int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions`).Scan(&failedVersions))
	if failedVersions != 0 {
		t.Fatal("audit fault published a PDF")
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER fail_pdf_audit ON authorization_audit`)
	historyCheck(t, err)
	first := call("POST", path+"/generate", intent, 200)["data"].(map[string]any)
	if first["version_id"] == nil {
		t.Fatal("missing PDF effect identity")
	}
	replayed := call("POST", path+"/generate", intent, 200)["data"].(map[string]any)
	if first["version_id"] != replayed["version_id"] {
		t.Fatal("retry regenerated PDF")
	}
	call("GET", "/pdf-operations/"+key, "", 200)
	client.Invoice.UpdateOne(i).SetNotes("Changed rendered notes").SaveX(ctx)
	staleKey := newOperationKey()
	stale := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, staleKey, j.ID, review)
	call("POST", path+"/generate", stale, 409)
	stored := call("GET", "/pdf-operations/"+staleKey, "", 200)["data"].(map[string]any)
	if stored["outcome"] != "source_drift" {
		t.Fatalf("receipt=%v", stored)
	}
	current := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
	dependent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[%q]}`, newOperationKey(), j.ID, current, staleKey)
	call("POST", path+"/generate", dependent, 409)
	// A changed review cannot reuse the identity of the rejected original intent.
	call("POST", path+"/generate", strings.Replace(stale, review, current, 1), 409)
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions WHERE document_id=$1`, i.ID).Scan(&count))
	if count != 1 {
		t.Fatalf("drift/dependency/replay published %d versions", count)
	}
}

func TestPDFGenerationIndependentGrantAndReceiptAfterScopeLossAPI(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	role, err := auth.SaveRole(ctx, actor, 0, "PDF generation only", []authorization.Grant{{Permission: "invoice.view", Scope: authorization.All}, {Permission: "invoice.pdf.generate", Scope: authorization.All}})
	historyCheck(t, err)
	u := client.User.Create().SetCompanyID(company).SetEmail("pdf-source@test").SetName("Independent PDF user").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, actor, u.ID, role))
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Private source customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("PDF Job").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("PDF independent").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, u.ID, "PDF only")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path, body string, status int) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s=%d: %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.String()
	}
	path := fmt.Sprintf("/invoices/%d/pdf", i.ID)
	var source struct {
		Data struct {
			Token string `json:"source_token"`
		}
	}
	historyCheck(t, json.Unmarshal([]byte(call("GET", path+"/source", "", 200)), &source))
	key := newOperationKey()
	intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, key, j.ID, source.Data.Token)
	first := call("POST", path+"/generate", intent, 200)
	expectStatus(t, web, sessionCookie(t, ctx, sessions, u.ID), "GET", path, 403)
	_, err = auth.SaveRole(ctx, actor, role, "PDF authority removed", nil, 1)
	historyCheck(t, err)
	call("GET", path+"/source", "", 404)
	if replay := call("POST", path+"/generate", intent, 200); replay != first {
		t.Fatal("scope loss changed receipt")
	}
	if receipt := call("GET", "/pdf-operations/"+key, "", 200); receipt != first {
		t.Fatal("lost-response lookup changed receipt")
	}
	assertNotContains(t, first, "Private source customer")
	assertNotContains(t, first, "PDF independent")
}

func TestPDFReferenceClientRealHTTP(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Reference PDF customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Reference Job").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Reference invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "PDF reference client")
	historyCheck(t, err)
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	command := exec.Command("node", "../api/v1/testdata/pdf-reference-conformance.cjs", server.URL, token, fmt.Sprint(i.ID))
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("reference: %v\n%s", err, output)
	}
	t.Log(string(output))
}
