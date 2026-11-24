package handlers

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
)

func TestFinancialAcceptanceEarlierInstallationPreservesHistoryGrantsAndBytes(t *testing.T) {
	pool, client, web, sessions, company, admin := permissionHTTPFixtureMigrations(t, onboardingMigrationsThrough(t, "124_lease_manual_adjustments.up.sql"))
	ctx := context.Background()
	var upgraded bool
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='125_service_entry_batch_receipts')`).Scan(&upgraded))
	if upgraded {
		t.Fatal("fixture is not an earlier installation")
	}
	c := client.Customer.Create().SetCompanyID(company).SetDisplayName("Installed financial history").SaveX(ctx)
	var issued int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM statuses WHERE category_key='invoice:invoiced' LIMIT 1`).Scan(&issued))
	paid := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(99500).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetStatusID(issued).SetTitle("Installed Payment allocation").SetLineItems(`[{"title":"Work","quantity":1,"unit_price":10}]`).SaveX(ctx)
	target := client.Invoice.Create().SetCompanyID(company).SetCustomerID(c.ID).SetInvoiceNumber(99501).SetInvoiceDate(time.Now()).SetDueDate(time.Now()).SetStatusID(issued).SetTitle("Installed Credit application").SetLineItems(`[{"title":"Work","quantity":1,"unit_price":20}]`).SaveX(ctx)
	a := authorization.Actor{UserID: admin, Channel: "web"}
	ctx = authorization.WithActor(ctx, authorization.New(pool), a)
	svc := settlement.New(pool)
	var today string
	historyCheck(t, pool.QueryRow(ctx, `SELECT (now() AT TIME ZONE timezone)::date::text FROM company_settings WHERE company_id=$1`, company).Scan(&today))
	allocated, err := svc.RecordInvoicePaymentWithCredit(ctx, a, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: paid.ID, AmountCents: 2000, Method: settlement.Cash, ReceivedDate: settlement.Date(today)})
	historyCheck(t, err)
	var source uuid.UUID
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE source_payment_id=$1`, allocated.ID).Scan(&source))
	_, err = svc.ApplyCredit(ctx, settlement.Actor{ID: admin, CompanyID: company}, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, InvoiceID: target.ID, CreditID: source, RequestedCents: 500, EffectiveDate: settlement.Date(today)})
	historyCheck(t, err)
	_, err = svc.RecordCustomerPayment(ctx, a, c.ID, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: newOperationKey()}, AmountCents: 10000, Method: settlement.Cash, ReceivedDate: "2020-01-01"})
	historyCheck(t, err)
	r, err := svc.RefundCredit(ctx, settlement.Actor{ID: admin, CompanyID: company}, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: newOperationKey()}, CustomerID: c.ID, AmountCents: 2000, Method: settlement.Cash, EffectiveDate: "2020-01-02", Reason: "Installed refund"})
	historyCheck(t, err)
	_, err = svc.ReverseRefund(ctx, settlement.Actor{ID: admin, CompanyID: company}, settlement.ReverseRequest{Operation: settlement.Operation{Key: newOperationKey()}, ID: r.ID, CustomerID: c.ID, EffectiveDate: "2020-01-03", Reason: "Installed reversal"})
	historyCheck(t, err)
	unknown := uuid.NewString()
	_, err = pool.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,amount_cents,method,received_date,created_at) VALUES($1,$2,$3,101,'cash','2019-01-01',NULL)`, unknown, company, c.ID)
	historyCheck(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date) VALUES($1,$2,$3,$4,101,'2019-01-01')`, uuid.NewString(), company, c.ID, unknown)
	historyCheck(t, err)
	_, err = authorization.New(pool).SaveRole(ctx, a, 0, "Installed operational role", []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.export", Scope: authorization.All}})
	historyCheck(t, err)
	cookie := sessionCookie(t, ctx, sessions, admin)
	path := masterPost(t, web, cookie, "/customers/export", url.Values{"operation_id": {newOperationKey()}}, 303)
	_, err = exports.Product(pool).ProcessOne(ctx)
	historyCheck(t, err)
	oldCSV := requestBody(t, web, cookie, "GET", path+"/download", 200)
	tables := []string{"invoices", "invoice_payments", "customer_credits", "payment_invoice_allocations", "credit_applications", "credit_refunds", "credit_refund_allocations", "settlement_reversals", "settlement_idempotency", "authorization_roles", "authorization_grants", "export_jobs", "export_records"}
	before := historyDurableRows(t, pool, tables)
	historyCheck(t, (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()))
	historyCheck(t, pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name='125_service_entry_batch_receipts')`).Scan(&upgraded))
	if !upgraded {
		t.Fatal("forward migration did not execute")
	}
	historyEqual(t, "financial identities, nullable evidence, sources, compensations, grants and historical artifacts", historyDurableRows(t, pool, tables), before)
	if got := requestBody(t, web, cookie, "GET", path+"/download", 200); got != oldCSV {
		t.Fatal("upgrade changed retained CSV bytes")
	}
	body := requestBody(t, web, cookie, "GET", "/reports/payments-credits", 200)
	assertContains(t, body, unknown)
	assertContains(t, body, "Unknown recording-time history")
	assertContains(t, body, "Installed financial history")
}
