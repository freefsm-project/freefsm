package handlers

import (
	"context"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestCommercialAdjustmentWinsRaceSettlementUsesAcceptedRevision(t *testing.T) {
	pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Adjustment race customer").SaveX(ctx)
	documentTransactionRequest(t, router, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Adjustment race"}, "invoice_date": {"2026-10-15"}, "tax_rate": {"0"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}}, 303)
	inv := client.Invoice.Query().OnlyX(ctx)
	actor := settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company, Role: "admin"}, inv.ID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/invoices/%d", inv.ID)
	values := url.Values{"customer_id": {fmt.Sprint(c.ID)}, "invoice_number": {fmt.Sprint(inv.InvoiceNumber)}, "title": {"Adjustment race"}, "adjustments": {`{"discount":{"kind":"percentage","value":"50"}}`}}
	values = commercialReviewedValues(t, router, cookie, path, values)
	fence, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fence.Rollback(context.Background())
	var blocker int
	if err := fence.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(190019)) barrier`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	edited := make(chan *httptest.ResponseRecorder, 1)
	go func() { edited <- jobRepairRawPost(router, cookie, path, values) }()
	commercialWaiters(t, ctx, pool, blocker, 1)
	paid := make(chan error, 1)
	go func() {
		_, err := settlement.New(pool).RecordPayment(ctx, actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: inv.ID, AmountCents: 2500, Method: settlement.Cash, ReceivedDate: settlement.Date(time.Now().Format("2006-01-02"))})
		paid <- err
	}()
	commercialWaiters(t, ctx, pool, blocker, 2)
	if err := fence.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	response := <-edited
	if response.Code != 303 {
		t.Fatalf("adjustment=%d: %s", response.Code, response.Body.String())
	}
	if err := <-paid; err != nil {
		t.Fatal(err)
	}
	view, err := settlement.New(pool).InvoiceSettlement(ctx, actor, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if view.TotalCents != 5000 || view.AmountDueCents != 2500 || view.SettledCents != 2500 {
		t.Fatalf("settlement used a different commercial revision: %+v", view)
	}
}
