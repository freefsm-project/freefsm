package handlers

import (
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/testutil"
	"github.com/google/uuid"
)

func TestOfficePaymentUpgradePreservesSourcesAndUnknownFacts(t *testing.T) {
	source := database.MigrationFS()
	old := fstest.MapFS{}
	files, err := fs.ReadDir(source, ".")
	historyCheck(t, err)
	for _, f := range files {
		if f.Name() >= "092_" {
			continue
		}
		b, err := fs.ReadFile(source, f.Name())
		historyCheck(t, err)
		old[f.Name()] = &fstest.MapFile{Data: b}
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, old)
	ctx := context.Background()
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed source balances").SaveX(ctx)
	var status int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='invoice:invoiced' LIMIT 1`).Scan(&status))
	i := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetStatusID(status).SetTitle("Installed legacy Invoice").SetInvoiceDate(time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)).SetDueDate(time.Time{}).SetLineItems(`[{"title":"Work","quantity":1,"unit_price":100}]`).SaveX(ctx)
	// Actual recorded sources: overpayment125, wholly unapplied30 (retains its
	// historic Invoice link), and a reversed wholly unapplied10. Unknown receipt
	// actor, entry time and label stay unknown; the migration supplies none.
	for n, amount := range []int{12500, 3000, 1000} {
		id := uuid.New()
		credit := uuid.New()
		allocated := 0
		if n == 0 {
			allocated = 10000
		}
		_, err = pool.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,invoice_id,amount_cents,method,received_date,reference,actor_id,created_at) VALUES($1,$2,$3,$4,$5,'cash','2020-01-02',$6,NULL,NULL)`, id, company, c.ID, i.ID, amount, fmt.Sprintf("historical-%d", n))
		historyCheck(t, err)
		if allocated > 0 {
			_, err = pool.Exec(ctx, `INSERT INTO payment_invoice_allocations VALUES($1,$2,$3)`, id, i.ID, allocated)
			historyCheck(t, err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date) VALUES($1,$2,$3,$4,$5,'2020-01-02')`, credit, company, c.ID, id, amount-allocated)
		historyCheck(t, err)
		if n == 2 {
			_, err = pool.Exec(ctx, `INSERT INTO settlement_reversals(id,company_id,operation_type,operation_id,reason,actor_id,effective_date) VALUES($1,$2,'payment',$3,'Installed reversal',$4,'2020-01-03')`, uuid.New(), company, id, admin)
			historyCheck(t, err)
		}
	}
	_, err = pool.Exec(ctx, `SELECT settlement_set_invoice_state($1,'paid')`, i.ID)
	historyCheck(t, err)
	tables := []string{"invoice_payments", "payment_invoice_allocations", "customer_credits", "authorization_grants", "invoices"}
	// Migration093 adds nullable reversal metadata, not historical facts. Compare
	// all installed columns and separately require the new values to stay unknown.
	reversalSnapshot := func() string {
		var raw string
		historyCheck(t, pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(r)-'reference'-'notes' ORDER BY r.id),'[]'::jsonb)::text FROM settlement_reversals r`).Scan(&raw))
		return raw
	}
	reversalsBefore := reversalSnapshot()
	before := preLeaseUpgradeRows(t, pool, tables)
	sequences := testutil.Sequences(t, ctx, pool)
	for range 2 {
		historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, source))
	}
	testutil.AssertNoInvoiceLeaseBackfill(t, ctx, pool)
	testutil.AssertSequenceUpgrade(t, ctx, pool, sequences, "lease_billing_obligations_id_seq", "leases_id_seq", "lease_billing_source_changes_id_seq", "lease_billing_obligation_revisions_id_seq", "lease_coverage_events_id_seq", "lease_coverage_revisions_id_seq", "lease_notice_revisions_id_seq", "lease_fact_corrections_id_seq", "lease_billing_revisions_id_seq", "lease_manual_adjustments_id_seq")
	historyEqual(t, "installed source identities, unknown facts, reversals and grants", preLeaseUpgradeRows(t, pool, tables), before)
	if reversalSnapshot() != reversalsBefore {
		t.Fatal("migration changed installed reversal history")
	}
	var invented int
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM settlement_reversals WHERE reference IS NOT NULL OR notes IS NOT NULL`).Scan(&invented))
	if invented != 0 {
		t.Fatal("migration invented historical reversal metadata")
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	page := documentTransactionRequest(t, web, cookie, "GET", fmt.Sprintf("/customers/%d/payments", c.ID), nil, 200)
	for _, s := range []string{"Unknown historical actor", "historical entry time unknown", "Installed reversal", "125.00", "30.00"} {
		assertContains(t, page.Body.String(), s)
	}
	documentTransactionRequest(t, web, cookie, "POST", fmt.Sprintf("/customers/%d/payments", c.ID), url.Values{"idempotency_key": {newOperationKey()}, "amount": {"12.34"}, "method": {"cash"}, "date": {"2026-01-02"}}, 303)
	// Migration rerun cannot generate a second source, including for the new
	// genuinely Invoice-less receipt.
	after := historyDurableRows(t, pool, tables)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, source))
	historyEqual(t, "new source is not replayed by migration", historyDurableRows(t, pool, tables), after)
	var available, received int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT sum(original_amount_cents) FROM customer_credits c WHERE NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=c.source_payment_id)),(SELECT sum(amount_cents) FROM invoice_payments)`).Scan(&available, &received))
	if available != 6734 || received != 17734 {
		t.Fatalf("source reconciliation available%d receipts%d", available, received)
	}
}
