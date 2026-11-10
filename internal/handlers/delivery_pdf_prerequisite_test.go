package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
)

func TestDeliveryPDFPrerequisiteAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Prerequisite customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Work").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Reviewed").SaveX(ctx)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
	historyCheck(t, err)
	token, err := sessions.CreateMobile(ctx, admin, "PDF prerequisite")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	call := func(method, path string, input any, status int) map[string]any {
		t.Helper()
		raw, err := json.Marshal(input)
		historyCheck(t, err)
		r := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		var out map[string]any
		historyCheck(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	source := call("GET", path+"/pdf/source", nil, 200)["data"].(map[string]any)["source_token"]
	pdfID := newOperationKey()
	pdfIntent := map[string]any{"operation_id": pdfID, "job_id": job.ID, "source_token": source, "depends_on": []string{}}
	version := call("POST", path+"/pdf/generate", pdfIntent, 200)["data"].(map[string]any)["version_id"]
	draft := map[string]any{"pdf_version_id": version, "confirm_stale_pdf": false, "to": []string{"recipient@example.test"}, "cc": []string{}, "bcc": []string{}, "subject": "Reviewed", "text_body": "Message", "html_body": "", "extra_file_ids": []int64{}}
	review := call("POST", path+"/deliveries/review", draft, 200)["data"].(map[string]any)
	draft["operation_id"] = newOperationKey()
	draft["job_id"] = job.ID
	draft["review_token"] = review["review_token"]
	draft["pdf_operation_id"] = newOperationKey()
	out := call("POST", path+"/deliveries", draft, 409)
	if out["error"].(map[string]any)["code"] != "invalid_dependency" {
		t.Fatal(out)
	}
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries`).Scan(&count))
	if count != 0 {
		t.Fatal("unknown prerequisite published Delivery")
	}
	unknownDelivery := draft["operation_id"].(string)
	// A failed source request cannot be bypassed with an existing valid PDF.
	failedPDF := newOperationKey()
	pdfIntent["operation_id"] = failedPDF
	pdfIntent["source_token"] = "no-longer-reviewed"
	call("POST", path+"/pdf/generate", pdfIntent, 409)
	draft["operation_id"] = newOperationKey()
	draft["pdf_operation_id"] = failedPDF
	out = call("POST", path+"/deliveries", draft, 409)
	if out["error"].(map[string]any)["code"] != "dependency_failed" {
		t.Fatal(out)
	}
	failedDelivery := draft["operation_id"].(string)
	call("POST", path+"/deliveries", draft, 409)
	receipt := call("GET", "/delivery-operations/"+failedDelivery, nil, 200)["data"].(map[string]any)
	if receipt["outcome"] != "dependency_failed" || receipt["delivery_ids"] != nil {
		t.Fatal(receipt)
	}
	// Changing just the prerequisite cannot repair the original intent identity.
	draft["pdf_operation_id"] = pdfID
	call("POST", path+"/deliveries", draft, 409)
	// Accepted receipts belonging to another client are not dependencies.
	originalToken := token
	token, err = sessions.CreateMobile(ctx, admin, "Foreign prerequisite client")
	historyCheck(t, err)
	draft["operation_id"] = newOperationKey()
	out = call("POST", path+"/deliveries", draft, 409)
	if out["error"].(map[string]any)["code"] != "invalid_dependency" {
		t.Fatal(out)
	}
	token = originalToken
	// Another document's successful generation is not this document's prerequisite.
	other := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Other document").SaveX(ctx)
	otherPath := fmt.Sprintf("/estimates/%d", other.ID)
	otherSource := call("GET", otherPath+"/pdf/source", nil, 200)["data"].(map[string]any)["source_token"]
	otherOperation := newOperationKey()
	call("POST", otherPath+"/pdf/generate", map[string]any{"operation_id": otherOperation, "job_id": job.ID, "source_token": otherSource, "depends_on": []string{}}, 200)
	draft["operation_id"] = newOperationKey()
	draft["pdf_operation_id"] = otherOperation
	out = call("POST", path+"/deliveries", draft, 409)
	if out["error"].(map[string]any)["code"] != "invalid_dependency" {
		t.Fatal(out)
	}
	draft["pdf_operation_id"] = pdfID
	// A newer accepted version cannot satisfy a dependency on the original one.
	pdfIntent["operation_id"] = newOperationKey()
	pdfIntent["source_token"] = source
	newVersion := call("POST", path+"/pdf/generate", pdfIntent, 200)["data"].(map[string]any)["version_id"]
	draft["operation_id"] = newOperationKey()
	draft["pdf_version_id"] = newVersion
	out = call("POST", path+"/deliveries", draft, 409)
	if out["error"].(map[string]any)["code"] != "invalid_dependency" {
		t.Fatal(out)
	}
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries`).Scan(&count))
	if count != 0 {
		t.Fatal("blocked prerequisite published Delivery")
	}
	// Publication and admission roll back together if audit fails.
	draft["operation_id"] = newOperationKey()
	draft["pdf_version_id"] = version
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_prerequisite_delivery_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='estimate.delivery.request' THEN RAISE EXCEPTION 'injected Delivery audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER prerequisite_delivery_audit_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_prerequisite_delivery_audit()`)
	historyCheck(t, err)
	call("POST", path+"/deliveries", draft, 503)
	call("GET", "/delivery-operations/"+draft["operation_id"].(string), nil, 404)
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM document_deliveries)+(SELECT count(*) FROM operation_admissions WHERE operation_id=$1)`, draft["operation_id"]).Scan(&count))
	if count != 0 {
		t.Fatal("failed publication retained outbox or admission")
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER prerequisite_delivery_audit_fault ON authorization_audit`)
	historyCheck(t, err)
	accepted := call("POST", path+"/deliveries", draft, 200)["data"].(map[string]any)
	call("POST", path+"/deliveries", draft, 200)
	var savedVersion int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT pdf_version_id FROM document_deliveries WHERE id=$1`, int64(accepted["delivery_ids"].([]any)[0].(float64))).Scan(&savedVersion))
	if savedVersion != int64(version.(float64)) {
		t.Fatal("accepted prerequisite substituted selected PDF")
	}
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries`).Scan(&count))
	if count != 1 {
		t.Fatal("replay duplicated Delivery")
	}
	draft["pdf_operation_id"] = pdfIntent["operation_id"]
	call("POST", path+"/deliveries", draft, 409)
	// Original terminal receipts remain terminal after a successful fresh intent.
	for id, outcome := range map[string]string{unknownDelivery: "invalid_dependency", failedDelivery: "dependency_failed"} {
		receipt := call("GET", "/delivery-operations/"+id, nil, 200)["data"].(map[string]any)
		if receipt["outcome"] != outcome {
			t.Fatal(receipt)
		}
	}
}
