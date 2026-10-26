package handlers

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
)

func TestPDFVersionInvalidSelectionNeverFallsBackHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Exact PDF selection").SaveX(ctx)
	for _, kind := range []string{"invoice", "estimate"} {
		t.Run(kind, func(t *testing.T) {
			fieldPost(t, router, cookie, "/"+kind+"s", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Selected version"}, "line_items": {"[]"}, "tax_rate": {"0"}}, 303)
			var id int64
			if err := pool.QueryRow(ctx, "SELECT id FROM "+kind+"s WHERE title='Selected version' AND company_id=$1", company).Scan(&id); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/%ss/%d", kind, id)
			fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
			requestBody(t, router, cookie, "GET", path+"/pdf", 200)
			for _, selection := range []string{"garbage", "0", "-1", "9223372036854775808", "", "1&version=2"} {
				expectStatus(t, router, cookie, "GET", path+"/pdf?version="+selection, 400)
			}
			expectStatus(t, router, cookie, "GET", path+"/pdf?version=999999", 404)
			for _, selection := range []string{"pdf_version=", "pdf_version=1&pdf_version=2", "pdf_version=1&version=2"} {
				expectStatus(t, router, cookie, "GET", path+"/email?"+selection, 422)
			}
		})
	}
}

func TestPDFVersionCapturesAllocatedAppliedAndDueHTTP(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	customer := client.Customer.Create().SetCompanyID(company).SetDisplayName("Captured settlement").SaveX(ctx)
	fieldPost(t, router, cookie, "/invoices", url.Values{"customer_id": {fmt.Sprint(customer.ID)}, "title": {"Settlement PDF"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var id int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices WHERE title='Settlement PDF'`).Scan(&id))
	path := fmt.Sprintf("/invoices/%d", id)
	fieldPost(t, router, cookie, path+"/finalize", commercialReviewedValues(t, router, cookie, path, url.Values{}), 303)
	svc := settlement.New(pool)
	actor := settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
	_, err := svc.RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: id, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: "2026-01-02"})
	historyCheck(t, err)
	fieldPost(t, router, cookie, fmt.Sprintf("/customers/%d/payments", customer.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"15.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	var credit uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE customer_id=$1`, customer.ID).Scan(&credit))
	application, err := svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: id, CreditID: credit, RequestedCents: 1500, EffectiveDate: "2026-01-02"})
	historyCheck(t, err)
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
	original := requestBody(t, router, cookie, "GET", path+"/pdf", 200)
	preview := requestBody(t, router, cookie, "GET", path+"/pdf/preview", 200)
	version := regexp.MustCompile(`version=([0-9]+)`).FindStringSubmatch(preview)
	if len(version) != 2 {
		t.Fatal("missing selected version")
	}
	checkAmounts := func(pdf, applied, due string) {
		t.Helper()
		command := exec.Command("pdftotext", "-layout", "-", "-")
		command.Stdin = strings.NewReader(pdf)
		text, err := command.CombinedOutput()
		historyCheck(t, err)
		for label, amount := range map[string]string{"Payments Allocated": "25.00", "Credit Applied": applied, "Amount Due": due} {
			if !regexp.MustCompile(regexp.QuoteMeta(label) + `:\s+\$` + regexp.QuoteMeta(amount)).Match(text) {
				t.Errorf("missing %s $%s in PDF: %s", label, amount, text)
			}
		}
	}
	checkAmounts(original, "15.00", "60.00")
	_, err = svc.ReverseCreditApplication(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: application.ID, InvoiceID: id, EffectiveDate: "2026-01-02", Reason: "Correct application"})
	historyCheck(t, err)
	expectStatus(t, router, cookie, "GET", path+"/pdf?version="+version[1], 409)
	fieldPost(t, router, cookie, path+"/pdf/save", url.Values{}, 303)
	checkAmounts(requestBody(t, router, cookie, "GET", path+"/pdf", 200), "0.00", "75.00")
	if old := requestBody(t, router, cookie, "GET", path+"/pdf?version="+version[1]+"&confirm_stale=1", 200); old != original {
		t.Fatal("reversal changed retained PDF bytes")
	}
}
