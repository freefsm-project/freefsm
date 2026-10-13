package handlers

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
)

func commercialUpload(t *testing.T, router http.Handler, cookie *http.Cookie, kind string, id int64) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "commercial-note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("Retained collaboration")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", fmt.Sprintf("/files?object_type=%s&object_id=%d", kind, id), &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("HX-Request", "true")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("collaboration upload=%d: %s", w.Code, w.Body.String())
	}
}

func TestCommercialCollaborationDoesNotInvalidateReviewedContentHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Collaboration customer").SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Collaborative Estimate"}, "line_items": {"[]"}, "tax_rate": {"0"}}
	documentTransactionRequest(t, router, cookie, "POST", "/estimates", values, 303)
	var id int64
	if err := pool.QueryRow(ctx, `SELECT id FROM estimates`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/estimates/%d", id)
	values = commercialReviewedValues(t, router, cookie, path, values)
	documentTransactionRequest(t, router, cookie, "POST", path+"/comments", url.Values{"content": {"Independent Comment"}}, 200)
	commercialUpload(t, router, cookie, "estimate", id)
	values.Set("notes", "Reviewed content survives collaboration")
	documentTransactionRequest(t, router, cookie, "POST", path, values, 303)
}

func TestCommercialSettlementLocksEveryExistingWriter(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Settled customer").SaveX(ctx)
	j := client.Job.Create().SetCompanyID(company).SetCustomerID(c.ID).SetJobType("Settled Job").SaveX(ctx)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "job_id": {fmt.Sprint(j.ID)}, "title": {"Locked Invoice"}, "invoice_date": {time.Now().Format("2006-01-02")}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", values, 303)
	var id, number int64
	if err := pool.QueryRow(ctx, `SELECT id,invoice_number FROM invoices`).Scan(&id, &number); err != nil {
		t.Fatal(err)
	}
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}, id); err != nil {
		t.Fatal(err)
	}
	if _, err := settlement.New(pool).RecordPayment(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: id, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: settlement.Date(time.Now().Format("2006-01-02"))}); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{`title='Changed'`, `notes='Changed'`, `terms='Changed'`, `invoice_date=invoice_date+1`, `due_date=due_date+1`, `job_id=NULL`, `line_items='[]'`, `tax_rate='9'`, `custom_fields='[{"definition_id":999,"value":"Changed"}]'`, `invoice_number=99999`} {
		if _, err := pool.Exec(ctx, `UPDATE invoices SET `+change+` WHERE id=$1`, id); err == nil {
			t.Fatalf("settlement allowed writer %s", change)
		}
	}
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(i)::text FROM invoices i WHERE id=$1`, id).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("rejected settled update persisted partial content")
	}
	path := fmt.Sprintf("/invoices/%d", id)
	documentTransactionRequest(t, router, cookie, "POST", path+"/comments", url.Values{"content": {"Settlement-independent Comment"}}, 200)
	commercialUpload(t, router, cookie, "invoice", id)
	var revisionBefore, revisionAfter int64
	if err := pool.QueryRow(ctx, `SELECT ($1::jsonb->>'commercial_revision')::bigint,commercial_revision FROM invoices WHERE id=$2`, before, id).Scan(&revisionBefore, &revisionAfter); err != nil {
		t.Fatal(err)
	}
	if revisionBefore != revisionAfter {
		t.Fatal("settlement-independent collaboration advanced commercial revision")
	}
}
