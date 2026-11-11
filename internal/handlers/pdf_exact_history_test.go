package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// This accepted source contains the old production PDF/Delivery producers, not
// merely an older subset of today's migration files.
const pdfHistoryProducer = "0ad7a81608426a76498d5f059df2fdb5401c52f9"

type pdfHistoricalExchange struct {
	Method, Path, Request, Response string
	Status                          int
}

type pdfHistoricalCapture struct {
	Schema, Token string
	Job           int64
	Documents     []string
	Reviews       []string
	Terminal      []pdfHistoricalExchange
	Rows          map[string]string
}

var pdfHistoricalTables = []string{"document_pdf_versions", "pdf_operations", "operation_admissions", "delivery_operations", "document_deliveries", "document_delivery_events", "authorization_audit", "authorization_grants", "issued_document_numbers", "customers", "jobs", "invoices", "estimates", "statuses", "invoice_payments", "payment_invoice_allocations", "customer_credits", "credit_applications", "settlement_idempotency", "settlement_reversals"}

func pdfHistoricalCall(t *testing.T, api http.Handler, token, method, path, body string, status int) pdfHistoricalExchange {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	exchange := pdfHistoricalExchange{method, path, body, w.Body.String(), w.Code}
	if out := os.Getenv("FREEFSM_PDF_HISTORY_PRODUCE"); out != "" {
		file, err := os.OpenFile(filepath.Join(out, "exchanges.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		historyCheck(t, err)
		err = json.NewEncoder(file).Encode(exchange)
		historyCheck(t, file.Close())
		historyCheck(t, err)
	}
	if w.Code != status {
		t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, status, w.Body.String())
	}
	return exchange
}

func pdfHistoricalData(t *testing.T, exchange pdfHistoricalExchange) map[string]any {
	t.Helper()
	var response struct{ Data map[string]any }
	historyCheck(t, json.Unmarshal([]byte(exchange.Response), &response))
	return response.Data
}

func pdfHistoricalCommand(t *testing.T, dir string, env []string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir, cmd.Env = dir, append(os.Environ(), env...)
	output, err := cmd.CombinedOutput()
	// Retain failed child output too: a later assertion must not discard the
	// historical producer's only standalone execution evidence.
	log, logErr := os.CreateTemp(dir, "command-"+filepath.Base(args[0])+"-*.log")
	historyCheck(t, logErr)
	_, logErr = log.Write(output)
	historyCheck(t, log.Close())
	historyCheck(t, logErr)
	if cmd.ProcessState != nil {
		t.Logf("command=%q exit=%d log=%s", args, cmd.ProcessState.ExitCode(), log.Name())
	}
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, output)
	}
	return output
}

// Both modes use this test entrypoint. The child is compiled inside an exact
// git archive with this test-only harness added; no production file is overlaid.
func TestPDFExactHistoricalProducerUpgradeReplayAPI(t *testing.T) {
	if os.Getenv("FREEFSM_TEST_DATABASE_URL") == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL")
	}
	if out := os.Getenv("FREEFSM_PDF_HISTORY_PRODUCE"); out != "" {
		pdfProduceHistorical(t, out)
		return
	}
	ctx := context.Background()
	dir := t.TempDir()
	if evidence := os.Getenv("FREEFSM_PDF_HISTORY_EVIDENCE"); evidence != "" {
		historyCheck(t, os.MkdirAll(evidence, 0700))
		var err error
		dir, err = os.MkdirTemp(evidence, "producer-")
		historyCheck(t, err)
		t.Logf("historical capture directory=%s", dir)
	}
	archive := filepath.Join(dir, "source.tar")
	cmd := exec.Command("git", "archive", "--format=tar", "--output="+archive, pdfHistoryProducer)
	cmd.Dir = "../.."
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("archive: %v %s", err, output)
	}
	source := filepath.Join(dir, "producer")
	historyCheck(t, os.Mkdir(source, 0700))
	pdfHistoricalCommand(t, dir, nil, "tar", "-xf", archive, "-C", source)
	harness, err := os.ReadFile("pdf_exact_history_test.go")
	historyCheck(t, err)
	historyCheck(t, os.WriteFile(filepath.Join(source, "internal/handlers/pdf_exact_history_test.go"), harness, 0600))
	archiveBytes, err := os.ReadFile(archive)
	historyCheck(t, err)
	historyEqual(t, "exact historical source archive", fmt.Sprintf("%x", sha256.Sum256(archiveBytes)), "e97332582f2959d49c372f461b38e0753ae703646fb4919a030d28d04560e278")
	t.Logf("historical production commit=%s archive_sha256=%x harness_sha256=%x", pdfHistoryProducer, sha256.Sum256(archiveBytes), sha256.Sum256(harness))
	output = pdfHistoricalCommand(t, source, []string{"FREEFSM_PDF_HISTORY_PRODUCE=" + dir}, "go", "test", "-race", "-json", "-count=1", "-timeout=10m", "./internal/handlers", "-run", "^TestPDFExactHistoricalProducerUpgradeReplayAPI$")
	historyCheck(t, os.WriteFile(filepath.Join(dir, "producer.jsonl"), output, 0600))
	t.Logf("historical producer raw events:\n%s", output)
	raw, err := os.ReadFile(filepath.Join(dir, "capture.json"))
	historyCheck(t, err)
	var capture pdfHistoricalCapture
	historyCheck(t, json.Unmarshal(raw, &capture))
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	root, err := pgxpool.New(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() {
		_, err := root.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{capture.Schema}.Sanitize()+" CASCADE")
		historyCheck(t, err)
		root.Close()
	})
	pdfHistoricalCommand(t, dir, nil, "psql", dsn, "-Xq", "-v", "ON_ERROR_STOP=1", "-f", filepath.Join(dir, "history.sql"))
	scoped, err := dsnWithSearchPath(dsn, capture.Schema)
	historyCheck(t, err)
	pool, err := pgxpool.New(ctx, scoped)
	historyCheck(t, err)
	t.Cleanup(pool.Close)
	config, err := pgx.ParseConfig(scoped)
	historyCheck(t, err)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, stdlib.OpenDB(*config))))
	t.Cleanup(func() { historyCheck(t, client.Close()) })
	api := apiv1.NewRouter(pool, client, services.NewSessionService(pool))
	historyEqual(t, "restored producer facts", historyDurableRows(t, pool, pdfHistoricalTables), capture.Rows)
	for range 2 {
		historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
		historyEqual(t, "upgrade preserves exact historical facts", historyDurableRows(t, pool, pdfHistoricalTables), capture.Rows)
	}
	for _, original := range capture.Terminal {
		replay := pdfHistoricalCall(t, api, capture.Token, original.Method, original.Path, original.Request, original.Status)
		// Error envelopes have a fresh HTTP request ID; the receipt fetched below
		// is the immutable operation result, not that transport correlation ID.
		var before, after map[string]any
		historyCheck(t, json.Unmarshal([]byte(original.Response), &before))
		historyCheck(t, json.Unmarshal([]byte(replay.Response), &after))
		for _, value := range []map[string]any{before, after} {
			if failure, ok := value["error"].(map[string]any); ok {
				delete(failure, "request_id")
			}
		}
		historyEqual(t, "original terminal response", after, before)
	}
	historyEqual(t, "terminal replay creates no effects", historyDurableRows(t, pool, pdfHistoricalTables), capture.Rows)
	for index, path := range capture.Documents {
		versions := pdfHistoricalData(t, pdfHistoricalCall(t, api, capture.Token, "GET", path+"/pdf/versions", "", 200))["versions"].([]any)
		if len(versions) != 1 || versions[0].(map[string]any)["stale"] != false {
			t.Fatalf("known historical source must remain current: %v", versions)
		}
		key := newOperationKey()
		intent := func(id, review string) string {
			return fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, id, capture.Job, review)
		}
		old := pdfHistoricalCall(t, api, capture.Token, "POST", path+"/pdf/generate", intent(key, capture.Reviews[index]), 409)
		assertContains(t, old.Response, "source_drift")
		current := pdfHistoricalData(t, pdfHistoricalCall(t, api, capture.Token, "GET", path+"/pdf/source", "", 200))["source_token"].(string)
		if current == capture.Reviews[index] {
			t.Fatal("old unaccepted review survived source identity upgrade")
		}
		pdfHistoricalCall(t, api, capture.Token, "POST", path+"/pdf/generate", intent(key, current), 409)
		repaired := pdfHistoricalData(t, pdfHistoricalCall(t, api, capture.Token, "POST", path+"/pdf/generate", intent(newOperationKey(), current), 200))
		historyEqual(t, "explicit repair accepted", repaired["outcome"], "accepted")
	}
	// Repairs append new facts; every original row, including immutable bytes,
	// intent bindings, audit and grants, must still exist exactly as captured.
	appended := map[string]int{"document_pdf_versions": 2, "pdf_operations": 4, "operation_admissions": 4, "authorization_audit": 2}
	current := historyDurableRows(t, pool, pdfHistoricalTables)
	for table, original := range capture.Rows {
		if extra := appended[table]; extra != 0 {
			var preserved bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT $1::jsonb @> $2::jsonb AND jsonb_array_length($1::jsonb)=jsonb_array_length($2::jsonb)+$3`, current[table], original, extra).Scan(&preserved))
			if !preserved {
				t.Fatalf("repair changed historical %s or appended unexpected facts", table)
			}
		} else {
			historyEqual(t, "repair preserves "+table, current[table], original)
		}
	}
}

func pdfProduceHistorical(t *testing.T, out string) {
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	var identityMigration int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations WHERE name='096_pdf_source_identity'`).Scan(&identityMigration))
	if identityMigration != 0 {
		t.Fatal("historical producer unexpectedly has source identity migration")
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Historical settlement customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Historical Job").SaveX(ctx)
	cookie := sessionCookie(t, ctx, sessions, admin)
	createInvoice := func(title string) int64 {
		fieldPost(t, web, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {title}, "line_items": {`[{"title":"Recorded work","quantity":1,"unit_price":100}]`}}, 303)
		var id int64
		historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE title=$1`, title).Scan(&id))
		path := fmt.Sprintf("/invoices/%d", id)
		fieldPost(t, web, cookie, path+"/finalize", commercialReviewedValues(t, web, cookie, path, url.Values{}), 303)
		return id
	}
	paid, invoice := createInvoice("Credit provenance"), createInvoice("Populated historical Invoice")
	financial := settlement.New(pool)
	actor := settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
	today := settlement.Date(time.Now().UTC().Format("2006-01-02"))
	_, err := financial.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: paid, AmountCents: 15000, Method: settlement.Transfer, ReceivedDate: today, Reference: "historical-bank-reference"})
	historyCheck(t, err)
	credit, err := financial.CustomerSettlement(ctx, actor, c.ID)
	historyCheck(t, err)
	if len(credit.Sources) != 1 {
		t.Fatal("expected historical overpayment credit")
	}
	_, err = financial.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, CreditID: credit.Sources[0].ID, RequestedCents: 1500, EffectiveDate: today})
	historyCheck(t, err)
	payment, err := financial.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: today, Reference: "historical-cash-original"})
	historyCheck(t, err)
	_, err = financial.ReversePayment(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, ID: payment.ID, EffectiveDate: today, Reason: "Correct cash reference", Reference: "historical-cash-correction"})
	historyCheck(t, err)
	_, err = financial.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: invoice, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: today, Reference: "historical-cash-replacement"})
	historyCheck(t, err)
	e := client.Estimate.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobID(j.ID).SetTitle("Populated historical Estimate").SetLineItems(`[{"title":"Proposed work","quantity":2,"unit_price":42}]`).SaveX(ctx)
	_, err = pool.Exec(ctx, `UPDATE company_settings SET smtp_from='sender@example.test',smtp_host='localhost' WHERE company_id=$1`, company)
	historyCheck(t, err)
	token, err := sessions.CreateMobile(ctx, admin, "Historical producer")
	historyCheck(t, err)
	api := apiv1.NewRouter(pool, client, sessions)
	capture := pdfHistoricalCapture{Token: token, Job: j.ID, Documents: []string{fmt.Sprintf("/invoices/%d", invoice), fmt.Sprintf("/estimates/%d", e.ID)}}
	historyCheck(t, pool.QueryRow(ctx, "SELECT current_schema()").Scan(&capture.Schema))
	checkpoint := func() {
		raw, err := json.MarshalIndent(capture, "", "  ")
		historyCheck(t, err)
		historyCheck(t, os.WriteFile(filepath.Join(out, "capture.json"), raw, 0600))
	}
	checkpoint()
	// Registered after the fixture: export partial history even on a later
	// assertion failure, before the fixture's schema cleanup executes.
	t.Cleanup(func() {
		pdfHistoricalCommand(t, out, nil, "pg_dump", os.Getenv("FREEFSM_TEST_DATABASE_URL"), "--no-owner", "--no-privileges", "--schema="+capture.Schema, "--file="+filepath.Join(out, "history.sql"))
	})
	for _, path := range capture.Documents {
		review := pdfHistoricalData(t, pdfHistoricalCall(t, api, token, "GET", path+"/pdf/source", "", 200))["source_token"].(string)
		capture.Reviews = append(capture.Reviews, review)
		pdfID := newOperationKey()
		intent := fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":%q,"depends_on":[]}`, pdfID, j.ID, review)
		accepted := pdfHistoricalCall(t, api, token, "POST", path+"/pdf/generate", intent, 200)
		historyEqual(t, "historical PDF accepted", pdfHistoricalData(t, accepted)["outcome"], "accepted")
		capture.Terminal = append(capture.Terminal, accepted)
		failedID := newOperationKey()
		rejected := pdfHistoricalCall(t, api, token, "POST", path+"/pdf/generate", fmt.Sprintf(`{"operation_id":%q,"job_id":%d,"source_token":"unreviewed","depends_on":[]}`, failedID, j.ID), 409)
		assertContains(t, rejected.Response, "source_drift")
		capture.Terminal = append(capture.Terminal, rejected)
		version := pdfHistoricalData(t, accepted)["version_id"]
		draft := map[string]any{"pdf_version_id": version, "confirm_stale_pdf": false, "to": []string{"recipient@example.test"}, "cc": []string{}, "bcc": []string{}, "subject": "Historical message", "text_body": "Original text", "html_body": "", "extra_file_ids": []int64{}}
		raw, err := json.Marshal(draft)
		historyCheck(t, err)
		deliveryReview := pdfHistoricalData(t, pdfHistoricalCall(t, api, token, "POST", path+"/deliveries/review", string(raw), 200))["review_token"]
		draft["job_id"], draft["review_token"] = j.ID, deliveryReview
		for _, status := range []int{200, 409} {
			draft["operation_id"] = newOperationKey()
			if status == 409 {
				draft["review_token"] = "unreviewed"
			}
			raw, err := json.Marshal(draft)
			historyCheck(t, err)
			capture.Terminal = append(capture.Terminal, pdfHistoricalCall(t, api, token, "POST", path+"/deliveries", string(raw), status))
			capture.Terminal = append(capture.Terminal, pdfHistoricalCall(t, api, token, "GET", "/delivery-operations/"+draft["operation_id"].(string), "", 200))
			outcome := "accepted"
			if status == 409 {
				outcome = "source_drift"
			}
			historyEqual(t, "historical Delivery receipt", pdfHistoricalData(t, capture.Terminal[len(capture.Terminal)-1])["outcome"], outcome)
		}
		for _, id := range []string{pdfID, failedID} {
			capture.Terminal = append(capture.Terminal, pdfHistoricalCall(t, api, token, "GET", "/pdf-operations/"+id, "", 200))
		}
		checkpoint()
	}
	capture.Rows = make(map[string]string)
	for _, table := range pdfHistoricalTables {
		capture.Rows[table] = historyDurableRows(t, pool, []string{table})[table]
		checkpoint()
	}
	var allocated, applied, reversed int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT
 (SELECT sum((a->>'amount_cents')::bigint) FROM jsonb_array_elements(source_snapshot->'allocations') a WHERE NOT (a->>'reversed')::boolean),
 (SELECT sum((a->>'amount_cents')::bigint) FROM jsonb_array_elements(source_snapshot->'credit_applications') a WHERE NOT (a->>'reversed')::boolean),
 (SELECT sum((a->>'amount_cents')::bigint) FROM jsonb_array_elements(source_snapshot->'allocations') a WHERE (a->>'reversed')::boolean)
 FROM document_pdf_versions WHERE document_type='invoice' AND document_id=$1`, invoice).Scan(&allocated, &applied, &reversed))
	historyEqual(t, "known active allocation", allocated, int64(2500))
	historyEqual(t, "known active application", applied, int64(1500))
	historyEqual(t, "known reversed allocation", reversed, int64(2500))
	var provenance bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM document_pdf_versions v, jsonb_array_elements(v.source_snapshot->'allocations') a
 JOIN invoice_payments p ON p.id=(a->>'payment_id')::uuid
 JOIN settlement_reversals r ON r.operation_type='payment' AND r.operation_id=p.id
 WHERE v.document_type='invoice' AND v.document_id=$1 AND p.reference='historical-cash-original' AND (a->>'reversed')::boolean AND r.reference='historical-cash-correction')
 AND EXISTS(SELECT 1 FROM document_pdf_versions v, jsonb_array_elements(v.source_snapshot->'allocations') a
 JOIN invoice_payments p ON p.id=(a->>'payment_id')::uuid
 WHERE v.document_type='invoice' AND v.document_id=$1 AND p.reference='historical-cash-replacement' AND NOT (a->>'reversed')::boolean)
 AND EXISTS(SELECT 1 FROM document_pdf_versions v, jsonb_array_elements(v.source_snapshot->'credit_applications') a
 JOIN customer_credits c ON c.id=(a->>'credit_id')::uuid JOIN invoice_payments p ON p.id=c.source_payment_id
 WHERE v.document_type='invoice' AND v.document_id=$1 AND p.reference='historical-bank-reference' AND NOT (a->>'reversed')::boolean)
 AND EXISTS(SELECT 1 FROM document_pdf_versions WHERE document_type='estimate' AND document_id=$2 AND source_snapshot->'document'->>'title'='Populated historical Estimate')`, invoice, e.ID).Scan(&provenance))
	if !provenance {
		t.Fatal("historical source lost known document or settlement provenance")
	}
	var knownDocuments int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM document_pdf_versions WHERE
 (document_type='invoice' AND document_id=$1 AND source_snapshot->'document'->>'title'='Populated historical Invoice' AND source_snapshot->'document'->'line_items' @> '[{"title":"Recorded work","quantity":1,"unit_price":100}]'::jsonb)
 OR (document_type='estimate' AND document_id=$2 AND source_snapshot->'document'->'line_items' @> '[{"title":"Proposed work","quantity":2,"unit_price":42}]'::jsonb)`, invoice, e.ID).Scan(&knownDocuments))
	historyEqual(t, "known document source snapshots", knownDocuments, 2)
	t.Logf("captured %d production terminal requests and responses, schema=%s", len(capture.Terminal), capture.Schema)
}
