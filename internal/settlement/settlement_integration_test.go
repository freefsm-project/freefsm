package settlement_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRecordPaymentIsAtomicAndIdempotentIntegration(t *testing.T) {
	dsn := os.Getenv("FREEFSM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FREEFSM_TEST_DATABASE_URL to run PostgreSQL settlement tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("freefsm_settlement_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`) })
	dsn = withSearchPath(t, dsn, schema)
	db, err := database.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(ctx, database.MigrationFS()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var companyID, customerID, actorID, invoiceID int64
	err = db.Pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Test','settlement-test') RETURNING id`).Scan(&companyID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Pool.Exec(ctx, `INSERT INTO company_settings(company_id,business_name,timezone) VALUES($1,'Test','UTC')`, companyID)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Customer') RETURNING id`, companyID).Scan(&customerID)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Pool.QueryRow(ctx, `INSERT INTO users(company_id,email,password_hash,name,role) VALUES($1,'settlement@example.test','x','Actor','admin') RETURNING id`, companyID).Scan(&actorID)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title,invoice_date,due_date,line_items,tax_rate) VALUES($1,$2,1,'Invoice',CURRENT_DATE,CURRENT_DATE,'[{"title":"Work","unit_price":100,"quantity":1}]','0') RETURNING id`, companyID, customerID).Scan(&invoiceID)
	if err != nil {
		t.Fatal(err)
	}

	svc := settlement.New(db.Pool)
	actor := settlement.Actor{ID: actorID, CompanyID: companyID, Role: "admin"}
	req := settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: "pay-1"}, InvoiceID: invoiceID, AmountCents: 12500, Method: settlement.Cash, ReceivedDate: settlement.Date(time.Now().Format("2006-01-02"))}
	results := make([]settlement.Result, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], errs[i] = svc.RecordPayment(ctx, actor, req) }(i)
	}
	wg.Wait()
	for _, err = range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if results[0] != results[1] {
		t.Fatalf("replay differs: %#v %#v", results[0], results[1])
	}
	var payments, activities int
	var state string
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM invoice_payments`).Scan(&payments)
	_ = db.Pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE action='payment_recorded'`).Scan(&activities)
	_ = db.Pool.QueryRow(ctx, `SELECT settlement_state FROM invoices WHERE id=$1`, invoiceID).Scan(&state)
	if payments != 1 || activities != 1 || state != "paid" || results[0].CreditCents != 2500 {
		t.Fatalf("payments=%d activities=%d state=%s result=%#v", payments, activities, state, results[0])
	}
	conflict := req
	conflict.AmountCents++
	if _, err = svc.RecordPayment(ctx, actor, conflict); !errors.Is(err, settlement.ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict = %v", err)
	}

	var secondInvoice int64
	err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title,invoice_date,due_date,line_items,tax_rate) VALUES($1,$2,2,'Second',CURRENT_DATE,CURRENT_DATE,'[{"title":"Work","unit_price":10,"quantity":1}]','0') RETURNING id`, companyID, customerID).Scan(&secondInvoice)
	if err != nil {
		t.Fatal(err)
	}
	var creditID uuid.UUID
	if err = db.Pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE source_payment_id=$1`, results[0].ID).Scan(&creditID); err != nil {
		t.Fatal(err)
	}
	today := settlement.Date(time.Now().Format("2006-01-02"))
	application, err := svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: "apply-1"}, InvoiceID: secondInvoice, CreditID: creditID, RequestedCents: 1000, EffectiveDate: today})
	if err != nil {
		t.Fatal(err)
	}
	refund, err := svc.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: "refund-1"}, CustomerID: customerID, AmountCents: 1000, Method: settlement.Check, EffectiveDate: today, Reason: "requested"})
	if err != nil {
		t.Fatal(err)
	}
	var otherCustomerID, otherInvoiceID int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Other Customer') RETURNING id`, companyID).Scan(&otherCustomerID); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title,invoice_date,due_date,line_items,tax_rate) VALUES($1,$2,3,'Other Invoice',CURRENT_DATE,CURRENT_DATE,'[{"title":"Work","unit_price":10,"quantity":1}]','0') RETURNING id`, companyID, otherCustomerID).Scan(&otherInvoiceID); err != nil {
		t.Fatal(err)
	}
	for name, reverse := range map[string]func() error{
		"payment": func() error {
			_, err := svc.ReversePayment(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "wrong-payment-parent"}, ID: results[0].ID, InvoiceID: otherInvoiceID, EffectiveDate: today, Reason: "correction"})
			return err
		},
		"credit application": func() error {
			_, err := svc.ReverseCreditApplication(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "wrong-application-parent"}, ID: application.ID, InvoiceID: otherInvoiceID, EffectiveDate: today, Reason: "correction"})
			return err
		},
		"refund": func() error {
			_, err := svc.ReverseRefund(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "wrong-refund-parent"}, ID: refund.ID, CustomerID: otherCustomerID, EffectiveDate: today, Reason: "correction"})
			return err
		},
	} {
		t.Run("rejects cross-parent "+name, func(t *testing.T) {
			if err := reverse(); !errors.Is(err, settlement.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
		})
	}
	if _, err = svc.ReversePayment(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "blocked"}, ID: results[0].ID, InvoiceID: invoiceID, EffectiveDate: today, Reason: "correction"}); !errors.Is(err, settlement.ErrDependency) {
		t.Fatalf("dependent reversal = %v", err)
	}
	if _, err = svc.ReverseRefund(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "reverse-refund"}, ID: refund.ID, CustomerID: customerID, EffectiveDate: today, Reason: "correction"}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ReverseCreditApplication(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "reverse-application"}, ID: application.ID, InvoiceID: secondInvoice, EffectiveDate: today, Reason: "correction"}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.ReversePayment(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: "reverse-payment"}, ID: results[0].ID, InvoiceID: invoiceID, EffectiveDate: today, Reason: "correction"}); err != nil {
		t.Fatal(err)
	}
	view, err := svc.CustomerSettlement(ctx, actor, customerID)
	if err != nil {
		t.Fatal(err)
	}
	if view.AvailableCreditCents != 0 {
		t.Fatalf("credit after source reversal = %d", view.AvailableCreditCents)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM activity_logs WHERE company_id=$1`, companyID).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if activities != 6 {
		t.Fatalf("activities = %d, want 6", activities)
	}
}

func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema)
		u.RawQuery = q.Encode()
		return u.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}
