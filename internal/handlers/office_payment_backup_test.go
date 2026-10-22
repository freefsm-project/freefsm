package handlers

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/config"
	"github.com/freefsm-project/freefsm/internal/middleware"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/jackc/pgx/v5"
)

func TestOfficePaymentBackupRestoresSourcesReversalsAndReceipts(t *testing.T) {
	dsn := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if dsn == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL disposable cluster required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	historyCheck(t, err)
	t.Cleanup(func() { historyCheck(t, admin.Close(ctx)) })
	src := newHistoryBackupInstance(t, admin, dsn, "Office source")
	dst := newHistoryBackupInstance(t, admin, dsn, "Office destination")
	tx, err := src.pool.Begin(ctx)
	historyCheck(t, err)
	defer tx.Rollback(ctx)
	var workflow int64
	historyCheck(t, tx.QueryRow(ctx, `INSERT INTO status_workflows(company_id,name,object_type) VALUES($1,'Invoices','invoice') RETURNING id`, src.company).Scan(&workflow))
	for _, category := range []string{"draft", "invoiced", "sent", "partially_paid", "paid", "void"} {
		_, err := tx.Exec(ctx, `INSERT INTO statuses(company_id,workflow_id,name,category_key,category_order,is_category_default) VALUES($1,$2,$3,$4,1,true)`, src.company, workflow, category, "invoice:"+category)
		historyCheck(t, err)
	}
	historyCheck(t, tx.Commit(ctx))
	web := middleware.Company(services.NewCompanySettingsService(src.client))(New(src.pool, src.client, src.sessions, &config.Config{UploadDir: t.TempDir(), MaxUploadSize: 25 << 20}))
	c := src.client.Customer.Create().SetCompanyID(src.company).SetDisplayName("Restore office Customer").SaveX(ctx)
	cookie := sessionCookie(t, ctx, src.sessions, src.actor)
	documentTransactionRequest(t, web, cookie, "POST", "/invoices", url.Values{"customer_id": {fmt.Sprint(c.ID)}, "title": {"Backup office Invoice"}, "invoice_date": {"2026-01-02"}, "line_items": {`[{"title":"Work","quantity":1,"unit_price":100}]`}, "tax_rate": {"0"}}, 303)
	var invoice int64
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id FROM invoices`).Scan(&invoice))
	historyCheck(t, statusflow.New(src.pool).FinalizeInvoice(ctx, statusflow.Actor{ID: src.actor, CompanyID: src.company}, invoice))
	path := fmt.Sprintf("/customers/%d/payments", c.ID)
	finance := settlement.New(src.pool)
	actor := authorization.Actor{UserID: src.actor, Channel: "web"}
	methods, err := finance.ConfiguredPaymentMethods(ctx, actor)
	historyCheck(t, err)
	historyCheck(t, finance.RenamePaymentMethod(ctx, actor, settlement.Cash, "Counter notes", methods[0].Revision))
	op := newOperationKey()
	values := url.Values{"idempotency_key": {op}, "amount": {"125.00"}, "method": {"cash"}, "date": {"2026-01-02"}, "invoice_id": {fmt.Sprint(invoice)}}
	documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
	values.Del("invoice_id")
	values.Set("idempotency_key", newOperationKey())
	values.Set("amount", "40.12")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
	values.Set("idempotency_key", newOperationKey())
	values.Set("amount", "9.87")
	documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
	var reversed string
	historyCheck(t, src.pool.QueryRow(ctx, `SELECT id::text FROM invoice_payments WHERE amount_cents=987`).Scan(&reversed))
	documentTransactionRequest(t, web, cookie, "POST", path+"/"+reversed+"/reverse", url.Values{"idempotency_key": {newOperationKey()}, "date": {"2026-01-02"}, "reason": {"Return before backup"}}, 303)
	methods, err = finance.ConfiguredPaymentMethods(ctx, actor)
	historyCheck(t, err)
	historyCheck(t, finance.RenamePaymentMethod(ctx, actor, settlement.Cash, "Next cash label", methods[0].Revision))
	tables := []string{"invoice_payments", "payment_invoice_allocations", "customer_credits", "settlement_reversals", "invoices", "settlement_idempotency", "operation_admissions", "payment_method_labels", "activity_logs"}
	before := historyDurableRows(t, src.pool, tables)
	lost := newOperationKey()
	src.roundTripTo(t, dst, func() {
		values.Set("idempotency_key", lost)
		values.Set("amount", "1.23")
		documentTransactionRequest(t, web, cookie, "POST", path, values, 303)
	})
	historyCheck(t, dst.manager.RefreshConnections())
	historyEqual(t, "receipt/source/reversal identities and activity", historyDurableRows(t, dst.pool, tables), before)
	web = middleware.Company(services.NewCompanySettingsService(dst.client))(New(dst.pool, dst.client, dst.sessions, &config.Config{UploadDir: t.TempDir(), MaxUploadSize: 25 << 20}))
	fresh := sessionCookie(t, ctx, dst.sessions, src.actor)
	lookup := documentTransactionRequest(t, web, fresh, "GET", "/payment-operations/"+op, nil, 200)
	assertContains(t, lookup.Body.String(), `"outcome":"accepted"`)
	documentTransactionRequest(t, web, fresh, "GET", "/payment-operations/"+lost, nil, 404)
	documentTransactionRequest(t, web, fresh, "POST", path, url.Values{"idempotency_key": {op}, "amount": {"125.00"}, "method": {"cash"}, "date": {"2026-01-02"}, "invoice_id": {fmt.Sprint(invoice)}}, 303)
	historyEqual(t, "restored replay never duplicates receipt or Credit", historyDurableRows(t, dst.pool, tables), before)
	page := documentTransactionRequest(t, web, fresh, "GET", path, nil, 200)
	for _, s := range []string{"125.00", "40.12", "9.87", "Return before backup", "Counter notes", "Next cash label"} {
		assertContains(t, page.Body.String(), s)
	}
	var available int64
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT sum(original_amount_cents) FROM customer_credits c WHERE NOT EXISTS(SELECT 1 FROM settlement_reversals r WHERE r.operation_type='payment' AND r.operation_id=c.source_payment_id)`).Scan(&available))
	if available != 6512 {
		t.Fatalf("restored available Credit=%d", available)
	}
	var snapshots int
	historyCheck(t, dst.pool.QueryRow(ctx, `SELECT count(*) FROM invoice_payments WHERE method='cash' AND method_label='Counter notes' AND channel='web' AND actor_id=$1 AND created_at IS NOT NULL`, src.actor).Scan(&snapshots))
	if snapshots != 3 {
		t.Fatalf("restored original method/attribution snapshots=%d", snapshots)
	}
}
