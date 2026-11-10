package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/settlement"
)

func TestPDFSourceDocumentABAAPI(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("ABA customer").SaveX(ctx)
	at := client.AssetType.Create().SetCompanyID(company).SetName("Source equipment").SaveX(ctx)
	asset := client.Asset.Create().SetCompanyID(company).SetCustomerID(c.ID).SetOwnershipClass("customer_owned").SetAssetTypeID(at.ID).SetName("Source asset").SetModel("Original model").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetAssetID(asset.ID).SetJobType("ABA Job").SaveX(ctx)
	var draft int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:draft'`, company).Scan(&draft))
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetStatusID(draft).SetTitle("Original title").SetLineItems(`[{"title":"Work","description":"Private description","quantity":1,"unit_price":100}]`).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Rendered reading"}, "object_type": {"invoice"}, "field_type": {"text"}, "include_pdf": {"true"}}, 303)
	fieldPost(t, web, cookie, "/settings/custom-fields", url.Values{"name": {"Private reading"}, "object_type": {"invoice"}, "field_type": {"text"}}, 303)
	_, err := pool.Exec(ctx, `UPDATE invoices SET custom_fields='[{"definition_id":1,"value":"original"},{"definition_id":2,"value":"private"}]' WHERE id=$1`, i.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `UPDATE company_settings SET pdf_show_line_item_descriptions=false WHERE company_id=$1`, company)
	historyCheck(t, err)
	token, err := sessions.CreateMobile(ctx, admin, "Source identity")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
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
	review := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
	intent := func(key, source string) string {
		return fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, key, j.ID, source)
	}
	first := call("POST", path+"/generate", intent(newOperationKey(), review), 200)
	client.Invoice.UpdateOne(i).SetTitle("Intervening title").SaveX(ctx)
	client.Invoice.UpdateOne(i).SetTitle("Original title").SaveX(ctx)
	key := newOperationKey()
	call("POST", path+"/generate", intent(key, review), 409)
	if receipt := call("GET", "/pdf-operations/"+key, "", 200)["data"].(map[string]any); receipt["outcome"] != "source_drift" {
		t.Fatalf("receipt=%v", receipt)
	}
	current := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
	if current == review {
		t.Fatal("ABA reused source identity")
	}
	call("POST", path+"/generate", intent(key, current), 409)
	call("POST", path+"/generate", intent(newOperationKey(), current), 200)
	versions := call("GET", path+"/versions", "", 200)
	encoded, err := json.Marshal(versions)
	historyCheck(t, err)
	assertNotContains(t, string(encoded), `"stale":true`)
	if first["data"].(map[string]any)["version_id"] == nil {
		t.Fatal("missing immutable version")
	}
	for _, tc := range []struct {
		name, change, restore string
		id                    int64
	}{
		{"customer", `UPDATE customers SET display_name='Changed customer' WHERE id=$1`, `UPDATE customers SET display_name='ABA customer' WHERE id=$1`, c.ID},
		{"job", `UPDATE jobs SET job_type='Changed job' WHERE id=$1`, `UPDATE jobs SET job_type='ABA Job' WHERE id=$1`, j.ID},
		{"rendered asset", `UPDATE assets SET model='Changed model' WHERE id=$1`, `UPDATE assets SET model='Original model' WHERE id=$1`, asset.ID},
		{"rendered field value", `UPDATE invoices SET custom_fields=jsonb_set(custom_fields,'{0,value}','"changed"') WHERE id=$1`, `UPDATE invoices SET custom_fields=jsonb_set(custom_fields,'{0,value}','"original"') WHERE id=$1`, i.ID},
		{"rendered field label", `UPDATE custom_field_definitions SET name='Changed reading' WHERE id=$1`, `UPDATE custom_field_definitions SET name='Rendered reading' WHERE id=$1`, 1},
		{"rendered field inclusion", `UPDATE custom_field_definitions SET include_pdf=false WHERE id=$1`, `UPDATE custom_field_definitions SET include_pdf=true WHERE id=$1`, 1},
		{"private field inclusion ABA", `UPDATE custom_field_definitions SET include_pdf=true WHERE id=$1`, `UPDATE custom_field_definitions SET include_pdf=false WHERE id=$1`, 2},
		{"presentation", `UPDATE company_settings SET invoice_footer='Changed footer' WHERE company_id=$1`, `UPDATE company_settings SET invoice_footer='' WHERE company_id=$1`, company},
		{"anchor", `UPDATE invoices SET job_id=NULL WHERE id=$1`, fmt.Sprintf(`UPDATE invoices SET job_id=%d WHERE id=$1`, j.ID), i.ID},
		{"status definition", `UPDATE statuses SET name='Temporarily renamed' WHERE id=$1`, `UPDATE statuses SET name='Draft' WHERE id=$1`, draft},
	} {
		t.Log(tc.name)
		before := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
		_, err := pool.Exec(ctx, tc.change, tc.id)
		historyCheck(t, err)
		_, err = pool.Exec(ctx, tc.restore, tc.id)
		historyCheck(t, err)
		call("POST", path+"/generate", intent(newOperationKey(), before), 409)
	}
	workflowReview := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
	documentPath := fmt.Sprintf("/invoices/%d", i.ID)
	fieldPost(t, web, cookie, documentPath+"/finalize", commercialReviewedValues(t, web, cookie, documentPath, url.Values{}), 303)
	fieldPost(t, web, cookie, documentPath+"/status", commercialReviewedValues(t, web, cookie, documentPath, url.Values{"status_id": {fmt.Sprint(draft)}}), 303)
	call("POST", path+"/generate", intent(newOperationKey(), workflowReview), 409)
	before := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
	commentPost(t, web, cookie, fmt.Sprintf("/invoices/%d/comments", i.ID), url.Values{"content": {"Not rendered"}, "operation_id": {newOperationKey()}}, 200)
	commercialUpload(t, web, cookie, "invoice", i.ID)
	var deliveryID int64
	historyCheck(t, pool.QueryRow(ctx, `INSERT INTO document_deliveries(company_id,document_type,document_id,invoice_id,actor_id,idempotency_key,request_fingerprint,recipients_to,subject,text_body,pdf_data,pdf_filename,message_id)
 VALUES($1,'invoice',$2,$2,$3,$4,decode(repeat('00',32),'hex'),ARRAY['recipient@example.test'],'Retained message','Retained text',$5,'retained.pdf','retained-evidence') RETURNING id`, company, i.ID, admin, newOperationKey(), []byte("retained PDF")).Scan(&deliveryID))
	_, err = pool.Exec(ctx, `INSERT INTO document_delivery_events(delivery_id,company_id,event_kind,evidence) VALUES($1,$2,'open','{"count":1}')`, deliveryID, company)
	historyCheck(t, err)
	for _, query := range []string{
		`UPDATE invoices SET custom_fields=jsonb_set(custom_fields,'{1,value}','"changed private value"') WHERE id=$1`,
		`UPDATE invoices SET line_items=jsonb_set(line_items,'{0,description}','"Changed private description"') WHERE id=$1`,
		`UPDATE custom_field_definitions SET name='Renamed private field' WHERE id=2 AND $1>0`,
		`UPDATE customers SET notes='Internal notes' WHERE id=(SELECT customer_id FROM invoices WHERE id=$1)`,
		`UPDATE jobs SET notes='Internal job notes' WHERE id=(SELECT job_id FROM invoices WHERE id=$1)`,
		`UPDATE custom_field_definitions SET help_text='Help only' WHERE id=1 AND $1>0`,
		`UPDATE company_settings SET invoice_prefix='FUTURE',email_auto_cc='office@example.test' WHERE company_id=(SELECT company_id FROM invoices WHERE id=$1)`,
	} {
		_, err := pool.Exec(ctx, query, i.ID)
		historyCheck(t, err)
	}
	after := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
	if before != after {
		t.Fatal("non-rendered mutations invalidated reviewed source")
	}
	call("POST", path+"/generate", intent(newOperationKey(), before), 200)
	// Immutable settlement evidence must reject a reviewed request even when
	// payment followed by reversal restores all displayed amounts.
	_, err = pool.Exec(ctx, `UPDATE invoices SET line_items='[{"title":"Work","quantity":1,"unit_price":100}]' WHERE id=$1`, i.ID)
	historyCheck(t, err)
	fieldPost(t, web, cookie, documentPath+"/finalize", commercialReviewedValues(t, web, cookie, documentPath, url.Values{}), 303)
	settlementReview := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
	settlementVersion := call("POST", path+"/generate", intent(newOperationKey(), settlementReview), 200)["data"].(map[string]any)["version_id"]
	svc := settlement.New(pool)
	actor := settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
	payment, err := svc.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: i.ID, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: "2026-01-02"})
	historyCheck(t, err)
	_, err = svc.ReversePayment(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: payment.ID, InvoiceID: i.ID, EffectiveDate: "2026-01-02", Reason: "Correct receipt"})
	historyCheck(t, err)
	call("POST", path+"/generate", intent(newOperationKey(), settlementReview), 409)
	for _, entry := range call("GET", path+"/versions", "", 200)["data"].(map[string]any)["versions"].([]any) {
		version := entry.(map[string]any)
		if version["id"] == settlementVersion && version["stale"] != false {
			t.Fatal("restored displayed settlement amounts are not content-equivalent")
		}
	}
}

func TestPDFEstimateSourceABAAPI(t *testing.T) {
	pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Estimate customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Estimate Job").SaveX(ctx)
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Original estimate").SaveX(ctx)
	token, err := sessions.CreateMobile(ctx, admin, "Estimate source")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/estimates/%d/pdf", e.ID)
	var review struct {
		Data struct {
			Token string `json:"source_token"`
		}
	}
	historyCheck(t, json.Unmarshal([]byte(pdfIdentityCall(t, api, token, "GET", path+"/source", "", 200)), &review))
	client.Estimate.UpdateOne(e).SetTitle("Intervening estimate").SaveX(ctx)
	client.Estimate.UpdateOne(e).SetTitle("Original estimate").SaveX(ctx)
	key := newOperationKey()
	intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, key, j.ID, review.Data.Token)
	assertContains(t, pdfIdentityCall(t, api, token, "POST", path+"/generate", intent, 409), "source_drift")
	assertContains(t, pdfIdentityCall(t, api, token, "GET", "/pdf-operations/"+key, "", 200), "source_drift")
}

func TestPDFSourceIdentityUpgradePreservesRecordedEvidenceAPI(t *testing.T) {
	prior := fstest.MapFS{}
	entries, err := fs.ReadDir(database.MigrationFS(), ".")
	historyCheck(t, err)
	for _, entry := range entries {
		if entry.Name() == "096_pdf_source_identity.up.sql" {
			continue
		}
		raw, err := fs.ReadFile(database.MigrationFS(), entry.Name())
		historyCheck(t, err)
		prior[entry.Name()] = &fstest.MapFile{Data: raw}
	}
	pool, client, _, sessions, company, admin := permissionHTTPFixtureMigrations(t, prior)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed source").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Installed Job").SaveX(ctx)
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Installed invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
	_, err = pool.Exec(ctx, `INSERT INTO document_pdf_versions(company_id,document_type,document_id,source_token,source_snapshot,filename,content,generated_by) VALUES($1,'invoice',$2,'retained-source','{}','retained.pdf',$3,$4)`, company, i.ID, []byte("%PDF-retained bytes"), admin)
	historyCheck(t, err)
	// Include complete domain rows, not just retained PDF evidence. PDF review
	// bookkeeping must not change legacy full-row projections or existing clocks.
	tables := []string{"document_pdf_versions", "authorization_grants", "issued_document_numbers", "customers", "jobs", "invoices", "statuses"}
	before := historyDurableRows(t, pool, tables)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyEqual(t, "source baseline preserves evidence", historyDurableRows(t, pool, tables), before)
	var revision, fieldRevisions, sourceRevisions int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT pdf_review_revision('invoices',$1),(SELECT count(*) FROM pdf_field_source_revisions),(SELECT count(*) FROM pdf_source_revisions)`, i.ID).Scan(&revision, &fieldRevisions, &sourceRevisions))
	if revision != 1 || fieldRevisions != 0 || sourceRevisions != 0 {
		t.Fatal("upgrade invented source history")
	}
	token, err := sessions.CreateMobile(ctx, admin, "Installed source review")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	path := fmt.Sprintf("/invoices/%d/pdf", i.ID)
	pdfIdentityCall(t, api, token, "GET", path+"/source", "", 200)
	assertContains(t, pdfIdentityCall(t, api, token, "GET", path+"/versions/1?confirm_stale=1", "", 200), "%PDF-retained bytes")
	historyEqual(t, "source read preserves retained bytes", historyDurableRows(t, pool, []string{"document_pdf_versions"})["document_pdf_versions"], before["document_pdf_versions"])
}

func pdfIdentityCall(t *testing.T, api http.Handler, token, method, path, body string, status int) string {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != status {
		t.Fatalf("%s %s = %d: %s", method, path, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func TestPDFPublicationFaultsRetrySameIdentityAPI(t *testing.T) {
	for _, fault := range []string{"storage", "audit", "receipt", "renderer"} {
		t.Run(fault, func(t *testing.T) {
			pool, client, _, sessions, company, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Fault customer").SaveX(ctx)
			j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Fault Job").SaveX(ctx)
			i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Durable invoice").SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SaveX(ctx)
			token, err := sessions.CreateMobile(ctx, admin, "Fault client")
			historyCheck(t, err)
			api := apiv1.NewRouterWithDelivery(pool, client, sessions, config.APIConfig{}, nil, delivery.New(pool, ""))
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
			if fault == "renderer" {
				logo := filepath.Join(t.TempDir(), "broken.png")
				historyCheck(t, os.WriteFile(logo, []byte("not a PNG"), 0600))
				_, err = pool.Exec(ctx, `UPDATE company_settings SET invoice_logo_path=$1 WHERE company_id=$2`, logo, company)
			} else {
				table := map[string]string{"storage": "document_pdf_versions", "audit": "authorization_audit", "receipt": "pdf_operations"}[fault]
				_, err = pool.Exec(ctx, `CREATE FUNCTION fail_pdf_publication() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected publication fault'; END $$; CREATE TRIGGER fail_pdf_publication BEFORE INSERT ON `+table+` FOR EACH ROW EXECUTE FUNCTION fail_pdf_publication()`)
			}
			historyCheck(t, err)
			review := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
			key := newOperationKey()
			intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, key, j.ID, review)
			call("POST", path+"/generate", intent, 503)
			call("GET", "/pdf-operations/"+key, "", 404)
			// Delivery cannot compose or queue against the failed generation's
			// nonexistent exact version; the API never substitutes current content.
			message := `{"pdf_version_id":1,"confirm_stale_pdf":false,"to":["recipient@example.test"],"cc":[],"bcc":[],"subject":"Dependent delivery","text_body":"Body","html_body":"","extra_file_ids":[]}`
			deliveryPath := fmt.Sprintf("/invoices/%d/deliveries", i.ID)
			call("POST", deliveryPath+"/review", message, 409)
			queued := strings.TrimSuffix(message, "}") + fmt.Sprintf(`,"operation_id":%q,"job_id":%d,"review_token":"unpublished-review"}`, newOperationKey(), j.ID)
			call("POST", deliveryPath, queued, 409)
			var versions, outbox, receipts, admissions, audits int
			historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM document_pdf_versions),(SELECT count(*) FROM document_deliveries),(SELECT count(*) FROM pdf_operations),(SELECT count(*) FROM operation_admissions WHERE operation_id=$1),(SELECT count(*) FROM authorization_audit WHERE action='invoice.pdf.generate')`, key).Scan(&versions, &outbox, &receipts, &admissions, &audits))
			if versions+outbox+receipts+admissions+audits != 0 {
				t.Fatalf("partial publication: versions=%d outbox=%d receipts=%d admissions=%d audits=%d", versions, outbox, receipts, admissions, audits)
			}
			if fault == "renderer" {
				// A retry of the unchanged request remains nonterminal. Fixing the
				// malformed source is new review, not permission to rewrite intent.
				call("POST", path+"/generate", intent, 503)
				_, err = pool.Exec(ctx, `UPDATE company_settings SET invoice_logo_path='' WHERE company_id=$1`, company)
				historyCheck(t, err)
				call("POST", path+"/generate", intent, 409)
				current := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
				call("POST", path+"/generate", strings.Replace(intent, review, current, 1), 409)
				intent = strings.Replace(strings.Replace(intent, key, newOperationKey(), 1), review, current, 1)
			} else {
				table := map[string]string{"storage": "document_pdf_versions", "audit": "authorization_audit", "receipt": "pdf_operations"}[fault]
				_, err = pool.Exec(ctx, `DROP TRIGGER fail_pdf_publication ON `+table)
				historyCheck(t, err)
			}
			dependentReview := call("GET", path+"/source", "", 200)["data"].(map[string]any)["source_token"].(string)
			dependent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[%q]}`, newOperationKey(), j.ID, dependentReview, key)
			call("POST", path+"/generate", dependent, 409)
			accepted := call("POST", path+"/generate", intent, 200)["data"].(map[string]any)
			replayed := call("POST", path+"/generate", intent, 200)["data"].(map[string]any)
			if accepted["version_id"] != replayed["version_id"] {
				t.Fatal("explicit retry duplicated version")
			}
			historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions`).Scan(&versions))
			if versions != 1 {
				t.Fatalf("versions=%d", versions)
			}
			// Parent recovery never silently revives a terminal descendant.
			call("POST", path+"/generate", dependent, 409)
		})
	}
}
