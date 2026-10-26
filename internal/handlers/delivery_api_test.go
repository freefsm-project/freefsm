package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func TestReviewedDeliveryAPIReceiptAndDrift(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Customer").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Work").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Reviewed").SaveX(ctx)
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
	originalPDF := requestBody(t, web, cookie, "GET", path+"/pdf?version="+version, 200)
	var versionID int64
	fmt.Sscan(version, &versionID)
	api := apiv1.NewRouter(pool, client, sessions)
	token, err := sessions.CreateMobile(ctx, admin, "Delivery client")
	historyCheck(t, err)
	call := func(method, path string, input any, status int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(input)
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
	disk := filepath.Join(t.TempDir(), "reviewed.txt")
	historyCheck(t, os.WriteFile(disk, []byte("reviewed bytes"), 0600))
	var fileID int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO files(company_id,object_type,object_id,original_name,stored_name,mime_type,file_size,file_path,uploaded_by) VALUES($1,'estimate',$2,'reviewed.txt','reviewed.txt','text/plain',14,$3,$4) RETURNING id`, company, doc.ID, disk, admin).Scan(&fileID))
	draft := map[string]any{"pdf_version_id": versionID, "confirm_stale_pdf": false, "to": []string{"one@example.test", "two@example.test"}, "cc": []string{}, "bcc": []string{}, "subject": "Reviewed", "text_body": "Message", "html_body": "", "extra_file_ids": []int64{fileID}}
	review := call("POST", path+"/deliveries/review", draft, 200)["data"].(map[string]any)
	intent := map[string]any{}
	for k, v := range draft {
		intent[k] = v
	}
	intent["operation_id"] = uuid.NewString()
	intent["review_token"] = review["review_token"]
	intent["job_id"] = job.ID
	intent["occurred_at"] = "2026-01-02T03:04:05Z"
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_delivery_review_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='estimate.delivery.request' THEN RAISE EXCEPTION 'injected delivery audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER delivery_review_audit_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_delivery_review_audit()`)
	historyCheck(t, err)
	call("POST", path+"/deliveries", intent, 503)
	call("GET", "/delivery-operations/"+intent["operation_id"].(string), nil, 404)
	var failedCount int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_deliveries`).Scan(&failedCount))
	if failedCount != 0 {
		t.Fatal("audit fault published fanout")
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER delivery_review_audit_fault ON authorization_audit`)
	historyCheck(t, err)
	first := call("POST", path+"/deliveries", intent, 200)["data"].(map[string]any)
	if len(first["delivery_ids"].([]any)) != 2 {
		t.Fatal(first)
	}
	receiptPath := "/delivery-operations/" + intent["operation_id"].(string)
	receipt := call("GET", receiptPath, nil, 200)["data"].(map[string]any)
	if fmt.Sprint(first) != fmt.Sprint(receipt) {
		t.Fatalf("receipt changed: %v / %v", first, receipt)
	}
	call("POST", path+"/deliveries", intent, 200)
	var attributed bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM authorization_audit WHERE detail->>'operation_id'=$1 AND detail->>'occurred_at'='2026-01-02T03:04:05Z')`, intent["operation_id"]).Scan(&attributed))
	if !attributed {
		t.Fatal("missing occurrence attribution")
	}
	originalToken := token
	token, err = sessions.CreateMobile(ctx, admin, "Other Delivery client")
	historyCheck(t, err)
	call("POST", path+"/deliveries", intent, 409)
	call("GET", receiptPath, nil, 404)
	token = originalToken
	call("POST", fmt.Sprintf("/invoices/%d/deliveries", doc.ID), intent, 409)
	intent["subject"] = "Changed"
	call("POST", path+"/deliveries", intent, 409)
	intent["subject"] = "Reviewed"
	acceptedKey := intent["operation_id"]
	intent["operation_id"] = uuid.NewString()
	historyCheck(t, os.WriteFile(disk, []byte("changed File bytes"), 0600))
	call("POST", path+"/deliveries", intent, 409)
	historyCheck(t, os.WriteFile(disk, []byte("reviewed bytes"), 0600))
	intent["operation_id"] = acceptedKey
	if _, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='replacement@example.test' WHERE company_id=$1`, company); err != nil {
		t.Fatal(err)
	}
	call("POST", path+"/deliveries", intent, 200) // accepted receipt, never recomposed
	intent["operation_id"] = uuid.NewString()
	call("POST", path+"/deliveries", intent, 409)
	rejected := call("GET", "/delivery-operations/"+intent["operation_id"].(string), nil, 200)["data"].(map[string]any)
	if rejected["outcome"] != "source_drift" {
		t.Fatal(rejected)
	}
	// Altered reviewed intent requires a new review and a new operation identity.
	call("POST", path+"/deliveries/review?version=%zz", draft, 400)
	values := url.Values{"pdf_version": {version}, "idempotency_key": {uuid.NewString()}, "to": {"web@example.test"}, "subject": {"Web"}, "body": {"Body"}}
	fieldPost(t, web, cookie, path+"/email", values, 303)
	server := httptest.NewServer(api)
	defer server.Close()
	bounded, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	output, err := exec.CommandContext(bounded, "node", "../api/v1/testdata/delivery-reference.cjs", server.URL, token, fmt.Sprint(doc.ID), version).CombinedOutput()
	t.Log(string(output))
	historyCheck(t, err)
	client.Estimate.UpdateOne(doc).SetTitle("Changed after exact version review").SaveX(ctx)
	stale := call("POST", path+"/deliveries/review", draft, 409)
	if stale["error"].(map[string]any)["code"] != "stale_pdf" {
		t.Fatalf("wrong stale outcome: %v", stale)
	}
	draft["confirm_stale_pdf"] = true
	intent["confirm_stale_pdf"] = true
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.delivery.request", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "Delivery without Generate", grants)
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetEmail("delivery-only@example.test").SetName("Delivery only").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, actor, user.ID, role))
	token, err = sessions.CreateMobile(ctx, user.ID, "Delivery independent grants")
	historyCheck(t, err)
	call("POST", path+"/deliveries/review", draft, 403)
	grants = append(grants, authorization.Grant{Permission: "estimate.pdf.download", Scope: authorization.All})
	var revision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
	_, err = auth.SaveRole(ctx, actor, role, "Delivery without Generate", grants, revision)
	historyCheck(t, err)
	call("POST", path+"/deliveries/review", draft, 403)
	grants = append(grants, authorization.Grant{Permission: "file.download", Scope: authorization.All})
	_, err = auth.SaveRole(ctx, actor, role, "Delivery without Generate", grants, revision+1)
	historyCheck(t, err)
	review = call("POST", path+"/deliveries/review", draft, 200)["data"].(map[string]any)
	intent["operation_id"] = uuid.NewString()
	intent["review_token"] = review["review_token"]
	acceptedStale := call("POST", path+"/deliveries", intent, 200)["data"].(map[string]any)
	var savedPDF []byte
	historyCheck(t, pool.QueryRow(ctx, `SELECT pdf_data FROM document_deliveries WHERE id=$1`, int64(acceptedStale["delivery_ids"].([]any)[0].(float64))).Scan(&savedPDF))
	if string(savedPDF) != originalPDF {
		t.Fatal("stale API substituted PDF bytes")
	}
	_, err = auth.SaveRole(ctx, actor, role, "Delivery without Generate", nil, revision+2)
	historyCheck(t, err)
	privateReceipt := call("GET", "/delivery-operations/"+intent["operation_id"].(string), nil, 200)["data"].(map[string]any)
	if len(privateReceipt) != 3 || privateReceipt["outcome"] != "accepted" {
		t.Fatalf("receipt privacy: %v", privateReceipt)
	}
	call("POST", path+"/deliveries/review", draft, 404)
}
