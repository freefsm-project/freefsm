package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"testing"
	"testing/fstest"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
)

func TestCreditApplicationInstalledHistoryUpgradeHTTP(t *testing.T) {
	source := database.MigrationFS()
	old := fstest.MapFS{}
	files, err := fs.ReadDir(source, ".")
	historyCheck(t, err)
	for _, file := range files {
		if file.Name() >= "093_" {
			continue
		}
		b, err := fs.ReadFile(source, file.Name())
		historyCheck(t, err)
		old[file.Name()] = &fstest.MapFile{Data: b}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	cookie := sessionCookie(t, ctx, sessions, admin)
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed Credit history").SaveX(ctx)
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"100.00"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Installed Application destination"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice int64
	var credit uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice))
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits`).Scan(&credit))
	historyCheck(t, statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin, CompanyID: company}, invoice))
	key := newOperationKey()
	application, err := settlement.New(pool).ApplyCredit(ctx, settlement.Actor{ID: admin, CompanyID: company, Role: "admin"}, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: key}, InvoiceID: invoice, CreditID: credit, RequestedCents: 6000, EffectiveDate: "2026-01-02"})
	historyCheck(t, err)
	// This is an actual pre093 schema. Record the old reversal shape, which had
	// no reference/note columns, without synthesizing any missing metadata.
	_, err = pool.Exec(ctx, `INSERT INTO settlement_reversals(id,company_id,operation_type,operation_id,reason,actor_id,effective_date) VALUES($1,$2,'credit_application',$3,'Installed correction',$4,'2026-01-02')`, uuid.New(), company, application.ID, admin)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `SELECT settlement_set_invoice_state($1,'unpaid')`, invoice)
	historyCheck(t, err)
	tables := []string{"credit_applications", "customer_credits", "invoice_payments", "settlement_idempotency", "invoices", "authorization_grants"}
	before := historyDurableRows(t, pool, tables)
	var oldReversal string
	historyCheck(t, pool.QueryRow(ctx, `SELECT to_jsonb(r)::text FROM settlement_reversals r`).Scan(&oldReversal))
	for range 2 {
		historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, source))
	}
	historyEqual(t, "installed Applications and grants unchanged", historyDurableRows(t, pool, tables), before)
	var after string
	var unknown bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT (to_jsonb(r)-'reference'-'notes')::text,reference IS NULL AND notes IS NULL FROM settlement_reversals r`).Scan(&after, &unknown))
	if after != oldReversal || !unknown {
		t.Fatal("upgrade rewrote reversal or invented metadata")
	}
	lookup := documentTransactionRequest(t, web, cookie, "GET", "/credit-application-operations/"+key, nil, 200)
	assertContains(t, lookup.Body.String(), `"outcome":"accepted"`)
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/invoices/%d/credit-applications", invoice), url.Values{"idempotency_key": {key}, "credit_id": {credit.String()}, "amount": {"60.00"}, "effective_date": {"2026-01-02"}}, 303)
	historyEqual(t, "installed replay preserves original reversed Application", historyDurableRows(t, pool, tables), before)
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/invoices/%d/credit-applications", invoice), url.Values{"idempotency_key": {key}, "credit_id": {credit.String()}, "amount": {"60.01"}, "effective_date": {"2026-01-02"}}, 409)
}
