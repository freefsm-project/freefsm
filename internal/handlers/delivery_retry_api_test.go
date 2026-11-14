package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/delivery"
)

func TestDeliveryRetryAPIAtomicReceipts(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Retry API").SaveX(ctx)
	job := client.Job.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobType("Work").SaveX(ctx)
	doc := client.Estimate.Create().SetCompanyID(company).SetCustomerID(customer.ID).SetJobID(job.ID).SetTitle("Frozen").SaveX(ctx)
	path := fmt.Sprintf("/estimates/%d", doc.ID)
	cookie := sessionCookie(t, ctx, sessions, admin)
	_, err := pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
	historyCheck(t, err)
	expectStatus(t, web, cookie, "POST", path+"/pdf/save", 303)
	version := compositionInput(t, requestBody(t, web, cookie, "GET", path+"/pdf/preview", 200), "version")
	conversionUpload(t, web, cookie, "estimate", doc.ID, "retry.txt", "frozen extra")
	var file int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM files`).Scan(&file))
	fieldPost(t, web, cookie, path+"/email", url.Values{"pdf_version": {version}, "idempotency_key": {newOperationKey()}, "to": {"retry@example.test"}, "subject": {"Frozen"}, "body": {"Body"}, "extra_file_id": {fmt.Sprint(file)}}, 303)
	svc := delivery.New(pool, "")
	frozen := func(d delivery.Delivery) string {
		raw, err := json.Marshal([]any{d.From, d.To, d.CC, d.BCC, d.Subject, d.TextBody, d.HTMLBody, d.PDF, d.PDFFilename, d.Attachments, d.MessageID, d.TrackingEnabled})
		historyCheck(t, err)
		return string(raw)
	}
	var originalSnapshot string
	_, err = svc.ProcessOne(ctx, deliveryTestSender(func(_ context.Context, d delivery.Delivery) (delivery.SendResult, error) {
		originalSnapshot = frozen(d)
		return delivery.SendResult{}, &delivery.SendError{Kind: delivery.SendPermanent, Err: errors.New("test permanent failure")}
	}), nil)
	historyCheck(t, err)
	var id int64
	var original string
	historyCheck(t, pool.QueryRow(ctx, `SELECT id,message_id FROM document_deliveries`).Scan(&id, &original))
	auth := authorization.New(pool)
	actor := authorization.Actor{UserID: admin}
	grants := []authorization.Grant{{Permission: "estimate.view", Scope: authorization.All}, {Permission: "estimate.delivery.request", Scope: authorization.All}}
	role, err := auth.SaveRole(ctx, actor, 0, "Retry API role", grants)
	historyCheck(t, err)
	user := client.User.Create().SetCompanyID(company).SetEmail("retry-api@test").SetName("Retry").SetPasswordHash("hash").SetRole("tech").SaveX(ctx)
	historyCheck(t, auth.AssignUser(ctx, actor, user.ID, role))
	token, err := sessions.CreateMobile(ctx, user.ID, "Retry client")
	historyCheck(t, err)
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	specResponse, err := http.Get(server.URL + "/openapi.json")
	historyCheck(t, err)
	var spec map[string]any
	historyCheck(t, json.NewDecoder(specResponse.Body).Decode(&spec))
	specResponse.Body.Close()
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	historyCheck(t, compiler.AddResource("https://retry.test/openapi.json", spec))
	schemas := map[string]*jsonschema.Schema{}
	for _, name := range []string{"DeliveryRetry", "ReviewedDelivery", "DeliveryReceiptEnvelope", "ErrorEnvelope"} {
		schemas[name], err = compiler.Compile("https://retry.test/openapi.json#/components/schemas/" + name)
		historyCheck(t, err)
	}
	call := func(method, path string, input any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(input)
		if method == "POST" {
			value, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
			historyCheck(t, err)
			name := "DeliveryRetry"
			if path != "/delivery-retries" {
				name = "ReviewedDelivery"
			}
			historyCheck(t, schemas[name].Validate(value))
		}
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(string(raw)))
		historyCheck(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		historyCheck(t, err)
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		historyCheck(t, err)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, want, body)
		}
		var out map[string]any
		historyCheck(t, json.Unmarshal(body, &out))
		value, err := jsonschema.UnmarshalJSON(strings.NewReader(string(body)))
		historyCheck(t, err)
		name := "ErrorEnvelope"
		if want == 200 {
			name = "DeliveryReceiptEnvelope"
		}
		historyCheck(t, schemas[name].Validate(value))
		return out
	}
	in := delivery.RetryIntent{OperationID: newOperationKey(), DocumentType: "estimate", DocumentID: doc.ID, JobID: job.ID, DeliveryID: id, Reason: "Retry frozen communication"}
	occurred := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	in.OccurredAt = &occurred
	call("POST", "/delivery-retries", in, 403)
	if got := call("GET", "/delivery-operations/"+in.OperationID, nil, 200)["data"].(map[string]any)["outcome"]; got != "forbidden" {
		t.Fatal(got)
	}
	var revision int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision))
	grants = append(grants, authorization.Grant{Permission: "estimate.pdf.download", Scope: authorization.All})
	_, err = auth.SaveRole(ctx, actor, role, "Retry API role", grants, revision)
	historyCheck(t, err)
	in.OperationID = newOperationKey()
	call("POST", "/delivery-retries", in, 403) // File Download is independent too.
	grants = append(grants, authorization.Grant{Permission: "file.download", Scope: authorization.All})
	_, err = auth.SaveRole(ctx, actor, role, "Retry API role", grants, revision+1)
	historyCheck(t, err)
	in.OperationID = newOperationKey()
	t.Run("original_document_lock_failure_is_nonterminal", func(t *testing.T) {
		// Admission's trigger applies only a transaction-local timeout. Holding the
		// original row forces the real retry FOR SHARE query to fail in PostgreSQL.
		_, err := pool.Exec(ctx, `CREATE FUNCTION retry_document_timeout() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM set_config('lock_timeout','100ms',true); RETURN NEW; END $$; CREATE TRIGGER retry_document_timeout BEFORE INSERT ON operation_admissions FOR EACH ROW EXECUTE FUNCTION retry_document_timeout()`)
		historyCheck(t, err)
		defer func() {
			_, err := pool.Exec(ctx, `DROP TRIGGER retry_document_timeout ON operation_admissions; DROP FUNCTION retry_document_timeout()`)
			historyCheck(t, err)
		}()
		lock, err := pool.Begin(ctx)
		historyCheck(t, err)
		defer lock.Rollback(ctx)
		_, err = lock.Exec(ctx, `SELECT id FROM estimates WHERE id=$1 FOR UPDATE`, doc.ID)
		historyCheck(t, err)
		probe := in
		probe.OperationID = newOperationKey()
		// Use the production HTTP router; do not synthesize an authorization error.
		raw, err := json.Marshal(probe)
		historyCheck(t, err)
		req, err := http.NewRequest("POST", server.URL+"/delivery-retries", strings.NewReader(string(raw)))
		historyCheck(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		historyCheck(t, err)
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		historyCheck(t, err)
		if res.StatusCode != 503 {
			t.Errorf("original document lock failure: HTTP %d want 503: %s", res.StatusCode, body)
		}
		var durable int
		historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_admissions WHERE operation_id=$1)+(SELECT count(*) FROM delivery_operations WHERE operation_id=$1)+(SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1::text)+(SELECT count(*) FROM document_delivery_events WHERE evidence->>'manual'='true')+(SELECT count(*) FROM activity_logs WHERE action='document_delivery_retried')`, probe.OperationID).Scan(&durable))
		if durable != 0 {
			t.Errorf("transient failure retained %d admission/receipt/audit/retry effects", durable)
		}
		var state string
		var cycle int
		historyCheck(t, pool.QueryRow(ctx, `SELECT state,retry_cycle FROM document_deliveries WHERE id=$1`, id).Scan(&state, &cycle))
		if state != "failed" || cycle != 0 {
			t.Errorf("transient failure changed delivery: %s cycle %d", state, cycle)
		}
		historyCheck(t, lock.Rollback(ctx))
		if t.Failed() {
			return
		}
		call("GET", "/delivery-operations/"+probe.OperationID, nil, 404)
		call("POST", "/delivery-retries", probe, 200)
		call("POST", "/delivery-retries", probe, 200)
		historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1`, probe.OperationID).Scan(&durable))
		if durable != 1 {
			t.Fatal("same-ID recovery audit count", durable)
		}
		// Return to a failed transport state for the independent audit regression.
		_, err = svc.ProcessOne(ctx, deliveryTestSender(func(context.Context, delivery.Delivery) (delivery.SendResult, error) {
			return delivery.SendResult{}, &delivery.SendError{Kind: delivery.SendPermanent, Err: errors.New("recovered retry failed transport")}
		}), nil)
		historyCheck(t, err)
	})
	_, err = pool.Exec(ctx, `CREATE FUNCTION fail_retry_api_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='estimate.delivery.retry' THEN RAISE EXCEPTION 'retry audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER retry_api_fault BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION fail_retry_api_audit()`)
	historyCheck(t, err)
	call("POST", "/delivery-retries", in, 503)
	call("GET", "/delivery-operations/"+in.OperationID, nil, 404)
	var count int
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_admissions WHERE operation_id=$1)+(SELECT count(*) FROM document_delivery_events WHERE evidence->>'manual'='true')`, in.OperationID).Scan(&count))
	if count != 1 { // Only the earlier recovered lock-failure retry remains.
		t.Fatal("audit failure retained admission or retry event")
	}
	_, err = pool.Exec(ctx, `DROP TRIGGER retry_api_fault ON authorization_audit`)
	historyCheck(t, err)
	// Concurrent actual HTTP admissions must yield identical receipts and one effect.
	raw, _ := json.Marshal(in)
	results := make(chan string, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", server.URL+"/delivery-retries", strings.NewReader(string(raw)))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- err.Error()
				return
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != 200 {
				results <- fmt.Sprintf("%d %s", res.StatusCode, body)
				return
			}
			var out struct {
				Data authorization.DeliveryReceipt `json:"data"`
			}
			if err := json.Unmarshal(body, &out); err != nil {
				results <- err.Error()
				return
			}
			encoded, _ := json.Marshal(out.Data)
			results <- string(encoded)
		}()
	}
	wg.Wait()
	a, b := <-results, <-results
	if a != b || !strings.Contains(a, `"outcome":"accepted"`) {
		t.Fatalf("duplicate admission: %s / %s", a, b)
	}
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_delivery_events WHERE evidence->>'manual'='true'`).Scan(&count))
	if count != 2 { // Recovery plus this concurrently admitted retry.
		t.Fatal("duplicate retry effects", count)
	}
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1 AND detail->>'occurred_at'='2026-01-02T03:04:05Z'`, in.OperationID).Scan(&count))
	if count != 1 {
		t.Fatal("retry audit attribution", count)
	}
	var lifetime, cycle int
	historyCheck(t, pool.QueryRow(ctx, `SELECT lifetime_attempt_count,cycle_attempt_count FROM document_deliveries WHERE id=$1`, id).Scan(&lifetime, &cycle))
	if lifetime != 2 || cycle != 2 {
		t.Fatal("automatic budget reset")
	}
	changed := in
	changed.Reason = "changed intent"
	call("POST", "/delivery-retries", changed, 409)
	changed = in
	changed.OperationID = newOperationKey()
	changed.JobID++
	if got := call("POST", "/delivery-retries", changed, 409)["error"].(map[string]any)["code"]; got != "source_drift" {
		t.Fatal(got)
	}
	changed = in
	changed.DocumentType = "invoice"
	call("POST", "/delivery-retries", changed, 409)
	var versionID int64
	fmt.Sscan(version, &versionID)
	call("POST", path+"/deliveries", map[string]any{"operation_id": in.OperationID, "review_token": "original-retry-is-not-creation", "job_id": job.ID, "pdf_version_id": versionID, "confirm_stale_pdf": false, "to": []string{"retry@example.test"}, "cc": []string{}, "bcc": []string{}, "subject": "Frozen", "text_body": "Body", "html_body": "", "extra_file_ids": []int64{file}}, 409)
	other, err := sessions.CreateMobile(ctx, user.ID, "Other client")
	historyCheck(t, err)
	originalToken := token
	token = other
	call("POST", "/delivery-retries", in, 409)
	call("GET", "/delivery-operations/"+in.OperationID, nil, 404)
	token = originalToken
	capture := &compositionSender{}
	worked, err := svc.ProcessOne(ctx, capture, nil)
	historyCheck(t, err)
	if !worked || len(capture.messages) != 1 || capture.messages[0].MessageID != original || string(capture.messages[0].Attachments[0].Data) != "frozen extra" {
		t.Fatal("frozen retry changed")
	}
	if frozen(capture.messages[0]) != originalSnapshot {
		t.Fatal("retry changed complete immutable snapshot")
	}
	noop := in
	noop.OperationID = newOperationKey()
	call("POST", "/delivery-retries", noop, 200)
	worked, err = svc.ProcessOne(ctx, capture, nil)
	historyCheck(t, err)
	if worked {
		t.Fatal("Accepted retry sent mail")
	}
	historyCheck(t, svc.RecordProviderEvidence(ctx, delivery.ProviderEvent{CompanyID: company, DeliveryID: id, ProviderIdentifier: original, EventID: "retry-bounce", State: "bounced", Evidence: map[string]any{"reason": "owned bounce"}}))
	bounce := in
	bounce.OperationID = newOperationKey()
	call("POST", "/delivery-retries", bounce, 200)
	historyCheck(t, pool.QueryRow(ctx, `SELECT lifetime_attempt_count,cycle_attempt_count FROM document_deliveries WHERE id=$1`, id).Scan(&lifetime, &cycle))
	if lifetime != 3 || cycle != 3 {
		t.Fatal("Bounced retry reset budget")
	}
	// Archive cancels prehandoff retry; restoring does not revive it.
	fieldPost(t, web, cookie, path+"/delete", url.Values{}, 303)
	fieldPost(t, web, cookie, path+"/restore", url.Values{}, 303)
	cancelled := in
	cancelled.OperationID = newOperationKey()
	if got := call("POST", "/delivery-retries", cancelled, 409)["error"].(map[string]any)["code"]; got != "cancelled" {
		t.Fatal(got)
	}
	call("POST", "/delivery-retries", bounce, 200) // prior terminal command still reconciles
	worked, err = svc.ProcessOne(ctx, capture, nil)
	historyCheck(t, err)
	if worked {
		t.Fatal("Cancelled retry restarted")
	}
	_, err = auth.SaveRole(ctx, actor, role, "Retry API role", nil, revision+2)
	historyCheck(t, err)
	private := call("GET", "/delivery-operations/"+in.OperationID, nil, 200)["data"].(map[string]any)
	if len(private) != 3 || private["outcome"] != "accepted" {
		t.Fatal("receipt privacy", private)
	}
	call("POST", "/delivery-retries", in, 200)
	in.OperationID = newOperationKey()
	call("POST", "/delivery-retries", in, 404)
}
