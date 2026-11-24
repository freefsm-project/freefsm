package authorization_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func financeCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func financeEvent(t *testing.T, events []services.FinancialActivityRow, kind, id string) services.FinancialActivityRow {
	t.Helper()
	var result services.FinancialActivityRow
	count := 0
	for _, event := range events {
		if event.Kind == kind && event.ID == id {
			result = event
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one %s event %s, got %d", kind, id, count)
	}
	return result
}
func financeContext(f commercialReportFixture) context.Context {
	f.actor.Channel = "web"
	return authorization.WithActor(context.Background(), f.svc, f.actor)
}
func financeReceipt(t *testing.T, f commercialReportFixture, cents int64, date string) settlement.Result {
	t.Helper()
	f.actor.Channel = "web"
	v, err := settlement.New(f.pool).RecordCustomerPayment(financeContext(f), f.actor, f.customer, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: uuid.NewString()}, AmountCents: cents, Method: settlement.Cash, ReceivedDate: settlement.Date(date)})
	financeCheck(t, err)
	return v
}
func financeReport(t *testing.T, f commercialReportFixture, kind authorization.FinancialReportKind, filter services.FinancialReportFilter) services.FinancialReport {
	t.Helper()
	v, err := services.ReadFinancialReport(context.Background(), f.pool, f.actor, kind, filter)
	financeCheck(t, err)
	return v
}
func financeInvoice(t *testing.T, f commercialReportFixture, number int64, category, due string, cents int64, job int64) int64 {
	t.Helper()
	var id int64
	lines := fmt.Sprintf(`[{"title":"Exact work","quantity":1,"unit_price":%d.%02d}]`, cents/100, cents%100)
	financeCheck(t, f.pool.QueryRow(context.Background(), `INSERT INTO invoices(company_id,customer_id,job_id,invoice_number,title,invoice_date,due_date,line_items,tax_rate,status_id) VALUES($1,$2,nullif($3,0),$4,'Financial Invoice',CURRENT_DATE,coalesce(nullif($5,'')::date,'0001-01-01'::date),$6::jsonb,'0',(SELECT id FROM statuses WHERE company_id=$1 AND category_key=$7 LIMIT 1)) RETURNING id`, f.company, f.customer, job, number, due, lines, "invoice:"+category).Scan(&id))
	return id
}
func financeReader(t *testing.T, f commercialReportFixture, name string, grants []authorization.Grant) authorization.Actor {
	t.Helper()
	ctx := context.Background()
	role, err := f.svc.SaveRole(ctx, f.actor, 0, name, grants)
	financeCheck(t, err)
	var user int64
	financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role,authorization_role_id) VALUES($1,$2,$2,'hash','tech',$3) RETURNING id`, f.company, name+"@test", role).Scan(&user))
	return authorization.Actor{UserID: user}
}
func financeGrants(extra ...authorization.Grant) []authorization.Grant {
	return append([]authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}}, extra...)
}

func TestFinancialReportImmutableCashFIFOAndPeriodIndependence(t *testing.T) {
	f := newCommercialReportFixture(t)
	f.actor.Channel = "web"
	ctx := financeContext(f)
	svc := settlement.New(f.pool)
	// Effective ordering differs from recording order; FIFO still uses source date.
	newer := financeReceipt(t, f, 6001, "2020-02-02")
	older := financeReceipt(t, f, 4003, "2020-01-01")
	actor := settlement.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}
	refund, err := svc.RefundCredit(ctx, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: uuid.NewString()}, CustomerID: f.customer, AmountCents: 5005, Method: settlement.Cash, EffectiveDate: "2020-03-01", Reason: "Returned externally"})
	financeCheck(t, err)
	r := financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{})
	if r.Totals.NetPaymentsCents != 4999 || r.Totals.AvailableCreditCents != 4999 || len(r.Activity) != 5 || len(r.Credits) != 1 {
		t.Fatalf("partial receipt/refund totals: %+v", r)
	}
	refundRow := financeEvent(t, r.Activity, "credit_refund", refund.ID.String())
	if len(refundRow.Allocations) != 2 || refundRow.Allocations[0].PaymentID != older.ID.String() || refundRow.Allocations[0].AmountCents != 4003 || refundRow.Allocations[1].PaymentID != newer.ID.String() || refundRow.Allocations[1].AmountCents != 1002 {
		t.Fatalf("FIFO allocation facts: %+v", refundRow)
	}
	oldPeriod := financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{From: "2019-01-01", To: "2019-12-31"})
	if len(oldPeriod.Activity) != 0 || oldPeriod.Totals.NetPaymentsCents != 0 || !reflect.DeepEqual(oldPeriod.Credits, r.Credits) {
		t.Fatal("period changed current Credit or selected backdated events")
	}
	reversed, err := svc.ReverseRefund(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: uuid.NewString()}, ID: refund.ID, CustomerID: f.customer, EffectiveDate: "2020-03-02", Reason: "Refund returned"})
	financeCheck(t, err)
	r = financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{})
	if r.Totals.NetPaymentsCents != 10004 || r.Totals.AvailableCreditCents != 10004 || len(r.Activity) != 6 {
		t.Fatalf("refund reversal=%+v", r)
	}
	e := financeEvent(t, r.Activity, "refund_reversal", reversed.ID.String())
	if e.OriginalKind != "credit_refund" || e.OriginalID != refund.ID.String() || len(e.Allocations) != 2 || e.Allocations[0].ID != refund.ID.String() || e.Allocations[0].Kind != "refund_credit" || e.Allocations[0].AmountCents != -4003 || e.Allocations[1].ID != refund.ID.String() || e.Allocations[1].Kind != "refund_credit" || e.Allocations[1].AmountCents != -1002 {
		t.Fatalf("compensating exact allocations=%+v", e)
	}
	for _, payment := range []settlement.Result{older, newer} {
		_, err := svc.ReverseUnappliedPayment(ctx, f.actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: uuid.NewString()}, ID: payment.ID, CustomerID: f.customer, EffectiveDate: "2020-03-03", Reason: "error"})
		financeCheck(t, err)
	}
	r = financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{})
	if r.Totals.NetPaymentsCents != 0 || r.Totals.AvailableCreditCents != 0 || len(r.Credits) != 0 || len(r.Activity) != 8 {
		t.Fatalf("full unwind=%+v", r)
	}
	var originals int
	financeCheck(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM invoice_payments`).Scan(&originals))
	if originals != 2 {
		t.Fatal("originals erased")
	}
}

func TestFinancialReportIndependentFacetsAndContributingInvoiceRefs(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	invoice := financeInvoice(t, f, 1, "invoiced", "", 12345, f.job)
	financeReceipt(t, f, 300, "2020-01-01")
	aging := financeReader(t, f, "Aging only", financeGrants(authorization.Grant{Permission: "customer.financial.export", Scope: authorization.All}, authorization.Grant{Permission: "invoice.view", Scope: authorization.Assigned}))
	_, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, aging.UserID)
	financeCheck(t, err)
	settled := financeReader(t, f, "History only", financeGrants(authorization.Grant{Permission: "settlement.view", Scope: authorization.All}, authorization.Grant{Permission: "settlement.export", Scope: authorization.All}))
	credit := financeReader(t, f, "Credit only", financeGrants(authorization.Grant{Permission: "credit.view", Scope: authorization.All}, authorization.Grant{Permission: "credit.export", Scope: authorization.All}))
	for _, test := range []struct {
		actor authorization.Actor
		kind  authorization.FinancialReportKind
	}{{aging, authorization.FinancialAging}, {aging, authorization.FinancialCustomers}, {settled, authorization.FinancialSettlement}, {credit, authorization.FinancialCredit}} {
		p, err := f.svc.Principal(ctx, test.actor)
		financeCheck(t, err)
		financeCheck(t, authorization.AuthorizeFinancialReport(p, test.kind, true))
		r, err := services.ReadFinancialReport(ctx, f.pool, test.actor, test.kind, services.FinancialReportFilter{})
		financeCheck(t, err)
		if len(r.References) == 0 {
			t.Fatal("missing authority contributors")
		}
	}
	for _, test := range []struct {
		actor authorization.Actor
		kind  authorization.FinancialReportKind
	}{{settled, authorization.FinancialCredit}, {credit, authorization.FinancialSettlement}, {credit, authorization.FinancialCombined}, {settled, authorization.FinancialCombined}} {
		_, err := services.ReadFinancialReport(ctx, f.pool, test.actor, test.kind, services.FinancialReportFilter{})
		if !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("independent facet exposed: %v", err)
		}
	}
	r, err := services.ReadFinancialReport(ctx, f.pool, aging, authorization.FinancialCustomers, services.FinancialReportFilter{})
	financeCheck(t, err)
	if r.Totals.ReceivableCents != 12345 || len(r.Customers) != 1 || len(r.Customers[0].Reference.Invoices) != 1 || r.Customers[0].Reference.Invoices[0].ID != invoice {
		t.Fatalf("summary contributor lost: %+v", r)
	}
	_, err = f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, aging.UserID)
	financeCheck(t, err)
	err = f.svc.WithinActor(ctx, aging, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckFinancialReference(ctx, tx, p, r.Customers[0].Reference)
	})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("aggregate artifact survived contributor authority loss: %v", err)
	}
	fresh, err := services.ReadFinancialReport(ctx, f.pool, aging, authorization.FinancialCustomers, services.FinancialReportFilter{})
	financeCheck(t, err)
	if fresh.Totals.ReceivableCents != 0 {
		t.Fatal("hidden Invoice influenced fresh totals")
	}
	_, err = services.ReadFinancialReport(ctx, f.pool, authorization.Actor{UserID: f.actor.UserID, Channel: "api"}, authorization.FinancialCombined, services.FinancialReportFilter{})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("API accepted: %v", err)
	}
}

func TestFinancialReportUnknownEntryAndCashRecordingBounds(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	// These inserted immutable facts model preserved legacy provenance; no UPDATE
	// or trigger suppression is used to invent a historical timestamp.
	unknown, known, source := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := f.pool.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,amount_cents,method,received_date,created_at) VALUES($1,$3,$4,101,'cash','2020-01-01',NULL),($2,$3,$4,10000,'cash','2020-01-01','2026-03-09 03:59:59+00')`, unknown, known, f.company, f.customer)
	financeCheck(t, err)
	_, err = f.pool.Exec(ctx, `INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date) VALUES($1,$2,$3,$4,101,'2020-01-01')`, source, f.company, f.customer, unknown)
	financeCheck(t, err)
	_, err = f.pool.Exec(ctx, `INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date) VALUES($1,$2,$3,$4,10000,'2020-01-01')`, uuid.NewString(), f.company, f.customer, known)
	financeCheck(t, err)
	_, err = f.pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York' WHERE company_id=$1`, f.company)
	financeCheck(t, err)
	r := financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{From: "2026-03-08", To: "2026-03-08"})
	if len(r.Activity) != 2 || r.Totals.NetPaymentsCents != 10000 || len(r.UnknownActivity) != 2 || r.Totals.UnknownEventCount != 2 || r.Totals.UnknownAmountCents != 202 || r.Totals.UnknownNetPaymentsCents != 101 || len(r.Credits) != 2 {
		t.Fatalf("unknown attribution or DST bounds=%+v", r)
	}
	for _, credit := range r.Credits {
		if credit.ID == source && credit.RecordedAt != nil {
			t.Fatal("Credit source invented entry time")
		}
	}
	for _, e := range r.UnknownActivity {
		if e.RecordedAt != nil {
			t.Fatal("unknown entry fabricated")
		}
	}
	loc, err := time.LoadLocation("America/New_York")
	financeCheck(t, err)
	start := time.Date(2026, 3, 8, 0, 0, 0, 0, loc)
	net, err := services.RecordedNetPayments(ctx, f.pool, f.company, start, start.AddDate(0, 0, 1))
	financeCheck(t, err)
	if net != 10000 {
		t.Fatalf("shared cash formula=%d", net)
	}
	next := financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{From: "2026-03-09", To: "2026-03-09"})
	if len(next.Activity) != 0 || len(next.UnknownActivity) != 2 || next.Totals.AvailableCreditCents != 10101 {
		t.Fatal("date changed unknown/current source")
	}
}

func TestFinancialReportFailureReturnsNoPartialSnapshot(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	// Both individual immutable receipts are valid int64 cents; their aggregate
	// cannot be represented. The second source must fail the whole report, not
	// return the first source as a successful partial balance.
	financeReceipt(t, f, math.MaxInt64, "2020-01-01")
	financeReceipt(t, f, 1, "2020-01-02")
	r, err := services.ReadFinancialReport(ctx, f.pool, f.actor, authorization.FinancialCredit, services.FinancialReportFilter{})
	if err == nil || !reflect.DeepEqual(r, services.FinancialReport{}) {
		t.Fatalf("partial success after bad second source: %+v %v", r, err)
	}
}

func TestFinancialReportProvidedTransactionSnapshot(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	financeReceipt(t, f, 10001, "2020-01-01")
	p, err := f.svc.Principal(ctx, f.actor)
	financeCheck(t, err)
	tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	financeCheck(t, err)
	defer tx.Rollback(ctx)
	first, err := services.FinancialReportSnapshot(ctx, tx, p, authorization.FinancialCombined, services.FinancialReportFilter{})
	financeCheck(t, err)
	// Another real command commits between two snapshot reads. This lower-level
	// transaction test isolates MVCC; the ordinary entry point additionally fences
	// authority before establishing its transaction.
	financeReceipt(t, f, 20002, "2020-01-02")
	second, err := services.FinancialReportSnapshot(ctx, tx, p, authorization.FinancialCombined, services.FinancialReportFilter{})
	financeCheck(t, err)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("provided transaction escaped to pool or mixed snapshots")
	}
	financeCheck(t, tx.Commit(ctx))
	fresh := financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{})
	if fresh.Totals.NetPaymentsCents != 30003 || fresh.Totals.AvailableCreditCents != 30003 {
		t.Fatalf("fresh source snapshot=%+v", fresh.Totals)
	}
}

func TestFinancialReportAgingDatabasePopulationAndAmounts(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `UPDATE company_settings SET timezone='America/New_York' WHERE company_id=$1`, f.company)
	financeCheck(t, err)
	var today string
	financeCheck(t, f.pool.QueryRow(ctx, `SELECT (clock_timestamp() AT TIME ZONE 'America/New_York')::date::text`).Scan(&today))
	date, err := time.Parse(time.DateOnly, today)
	financeCheck(t, err)
	var want [6]int64
	for i, test := range []struct{ days, bucket int }{{-1, 0}, {0, 0}, {1, 1}, {30, 1}, {31, 2}, {60, 2}, {61, 3}, {90, 3}, {91, 4}} {
		financeInvoice(t, f, int64(i+1), "invoiced", date.AddDate(0, 0, -test.days).Format(time.DateOnly), 101, 0)
		want[test.bucket] += 101
	}
	financeInvoice(t, f, 10, "sent", "", 103, 0)
	want[5] = 103
	financeInvoice(t, f, 11, "draft", today, 99999, 0)
	financeInvoice(t, f, 12, "void", today, 99999, 0)
	financeInvoice(t, f, 13, "invoiced", today, 0, 0)
	paid := financeInvoice(t, f, 14, "invoiced", today, 10001, 0)
	actor := f.actor
	actor.Channel = "web"
	_, err = settlement.New(f.pool).RecordInvoicePaymentWithCredit(financeContext(f), actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: uuid.NewString()}, InvoiceID: paid, AmountCents: 10001, Method: settlement.Cash, ReceivedDate: settlement.Date(today)})
	financeCheck(t, err)
	r := financeReport(t, f, authorization.FinancialAging, services.FinancialReportFilter{})
	if r.Totals.Buckets != want || r.Totals.InvoiceCount != 10 || r.Totals.ReceivableCents != 1012 || r.Totals.TotalCents != 1012 || r.Totals.SettledCents != 0 {
		t.Fatalf("aging eligible totals=%+v", r.Totals)
	}
	for _, invoice := range r.Invoices {
		if invoice.ID == paid {
			t.Fatal("fully settled Invoice included")
		}
	}
	filtered := financeReport(t, f, authorization.FinancialAging, services.FinancialReportFilter{Bucket: "31_60"})
	if len(filtered.Invoices) != 2 || filtered.Totals.ReceivableCents != 202 || filtered.Totals.InvoiceCount != 2 {
		t.Fatalf("bucket totals=%+v", filtered)
	}
	customer := financeReport(t, f, authorization.FinancialCustomers, services.FinancialReportFilter{})
	if customer.Totals != r.Totals || len(customer.Customers[0].Reference.Invoices) != 10 {
		t.Fatal("Customer/Aging eligible sets or contributor refs disagree")
	}
}

func TestFinancialReportApplicationsAndOriginalAllocationIdentities(t *testing.T) {
	f := newCommercialReportFixture(t)
	f.actor.Channel = "web"
	ctx := financeContext(f)
	svc := settlement.New(f.pool)
	invoice := financeInvoice(t, f, 1, "invoiced", "", 10000, 0)
	destination := financeInvoice(t, f, 2, "invoiced", "", 10000, 0)
	payment, err := svc.RecordInvoicePaymentWithCredit(ctx, f.actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: uuid.NewString()}, InvoiceID: invoice, AmountCents: 15001, Method: settlement.Cash, ReceivedDate: "2020-01-01"})
	financeCheck(t, err)
	var credit uuid.UUID
	financeCheck(t, f.pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE source_payment_id=$1`, payment.ID).Scan(&credit))
	actor := settlement.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}
	application, err := svc.ApplyCredit(ctx, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: uuid.NewString()}, InvoiceID: destination, CreditID: credit, RequestedCents: 2003, EffectiveDate: "2020-01-02"})
	financeCheck(t, err)
	r := financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{})
	if r.Totals.NetPaymentsCents != 15001 || r.Totals.AvailableCreditCents != 2998 {
		t.Fatalf("application double-counted cash: %+v", r.Totals)
	}
	aging := financeReport(t, f, authorization.FinancialAging, services.FinancialReportFilter{})
	if aging.Totals.ReceivableCents != 7997 || aging.Totals.SettledCents != 2003 {
		t.Fatalf("partial settlement aging=%+v", aging.Totals)
	}
	e := financeEvent(t, r.Activity, "payment", payment.ID.String())
	if len(e.Allocations) != 1 || e.Allocations[0].ID != payment.ID.String() || e.Allocations[0].Kind != "payment_invoice" || e.Allocations[0].InvoiceID != invoice || e.Allocations[0].PaymentID != payment.ID.String() || e.Allocations[0].AmountCents != 10000 {
		t.Fatalf("payment allocation=%+v", e)
	}
	e = financeEvent(t, r.Activity, "credit_application", application.ID.String())
	if len(e.Allocations) != 1 || e.Allocations[0].ID != application.ID.String() || e.Allocations[0].Kind != "credit_application" || e.Allocations[0].InvoiceID != destination || e.Allocations[0].CreditID != credit.String() || e.Allocations[0].PaymentID != payment.ID.String() || e.Allocations[0].AmountCents != 2003 {
		t.Fatalf("application allocation=%+v", e)
	}
	reversed, err := svc.ReverseCreditApplication(ctx, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: uuid.NewString()}, ID: application.ID, InvoiceID: destination, EffectiveDate: "2020-01-03", Reason: "Application error"})
	financeCheck(t, err)
	r = financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{})
	if r.Totals.NetPaymentsCents != 15001 || r.Totals.AvailableCreditCents != 5001 {
		t.Fatal("application reversal changed cash")
	}
	e = financeEvent(t, r.Activity, "application_reversal", reversed.ID.String())
	if e.OriginalKind != "credit_application" || e.OriginalID != application.ID.String() || len(e.Allocations) != 1 || e.Allocations[0].ID != application.ID.String() || e.Allocations[0].Kind != "credit_application" || e.Allocations[0].InvoiceID != destination || e.Allocations[0].CreditID != credit.String() || e.Allocations[0].PaymentID != payment.ID.String() || e.Allocations[0].AmountCents != -2003 {
		t.Fatalf("compensating allocation=%+v", e)
	}
}
