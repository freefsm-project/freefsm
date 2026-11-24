package authorization_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func financeReferenceCheck(f commercialReportFixture, actor authorization.Actor, ref authorization.FinancialReference) error {
	return f.svc.WithinActor(context.Background(), actor, func(tx pgx.Tx, p authorization.Principal) error {
		return authorization.CheckFinancialReference(context.Background(), tx, p, ref)
	})
}
func financeDenied(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, authorization.ErrForbidden) && !errors.Is(err, authorization.ErrNotFound) {
		t.Fatalf("expected nondisclosing denial, got %v", err)
	}
}

func TestFinancialReportOriginalAndCurrentJobReferenceTransitions(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	invoice := financeInvoice(t, f, 1, "invoiced", "", 10001, f.job)
	reader := financeReader(t, f, "Assigned finance transitions", financeGrants(authorization.Grant{Permission: "invoice.view", Scope: authorization.Assigned}))
	_, err := f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, reader.UserID)
	financeCheck(t, err)
	r, err := services.ReadFinancialReport(ctx, f.pool, reader, authorization.FinancialAging, services.FinancialReportFilter{})
	financeCheck(t, err)
	if len(r.Invoices) != 1 || r.Invoices[0].ID != invoice {
		t.Fatalf("missing initial Invoice: %+v", r)
	}
	ref := r.Invoices[0].Reference
	financeCheck(t, financeReferenceCheck(f, reader, ref))
	// Commit ordinary anchor changes in PostgreSQL, retaining both historical
	// original and current references. No grant object or principal is mocked.
	_, err = f.pool.Exec(ctx, `UPDATE invoices SET job_id=$1 WHERE id=$2`, f.otherJob, invoice)
	financeCheck(t, err)
	financeDenied(t, financeReferenceCheck(f, reader, ref)) // current Job inaccessible
	_, err = f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.otherJob, reader.UserID)
	financeCheck(t, err)
	financeCheck(t, financeReferenceCheck(f, reader, ref)) // both anchors authorized
	_, err = f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, f.job, reader.UserID)
	financeCheck(t, err)
	financeDenied(t, financeReferenceCheck(f, reader, ref)) // original Job lost
	fresh, err := services.ReadFinancialReport(ctx, f.pool, reader, authorization.FinancialAging, services.FinancialReportFilter{})
	financeCheck(t, err)
	if len(fresh.Invoices) != 1 || fresh.Invoices[0].Reference.Invoices[0].JobID != f.otherJob {
		t.Fatal("current selection must remain distinct from historical artifact authority")
	}
	financeCheck(t, financeReferenceCheck(f, reader, fresh.Invoices[0].Reference))
	_, err = f.pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, f.job, reader.UserID)
	financeCheck(t, err)
	var otherCustomer int64
	financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Other anchor Customer') RETURNING id`, f.company).Scan(&otherCustomer))
	_, err = f.pool.Exec(ctx, `UPDATE jobs SET customer_id=$1 WHERE id=$2`, otherCustomer, f.job)
	financeCheck(t, err)
	financeDenied(t, financeReferenceCheck(f, reader, ref)) // original Customer mismatch
	financeCheck(t, financeReferenceCheck(f, reader, fresh.Invoices[0].Reference))
	// The installed domain guard rejects changing the currently linked Job's
	// Customer while this finalized Invoice remains attached. Do not manufacture
	// an impossible current mismatch by bypassing that guard.
	_, err = f.pool.Exec(ctx, `UPDATE jobs SET customer_id=$1 WHERE id=$2`, otherCustomer, f.otherJob)
	var constraint *pgconn.PgError
	if !errors.As(err, &constraint) || constraint.Code != "23514" {
		t.Fatalf("current anchor guard=%v", err)
	}
	financeCheck(t, financeReferenceCheck(f, reader, fresh.Invoices[0].Reference))
	_, err = f.pool.Exec(ctx, `UPDATE invoices SET job_id=NULL WHERE id=$1`, invoice)
	financeCheck(t, err)
	financeDenied(t, financeReferenceCheck(f, reader, fresh.Invoices[0].Reference))
}

func TestFinancialReportArchivedCustomerHistoryAndRestoredReferences(t *testing.T) {
	f := newCommercialReportFixture(t)
	f.actor.Channel = "web"
	ctx := financeContext(f)
	// Independent Customer with no operational children can be archived through
	// the installed reviewed cascade after fully unwinding its receipt.
	financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Archived financial history') RETURNING id`, f.company).Scan(&f.customer))
	payment := financeReceipt(t, f, 20001, "2020-01-01")
	reversal, err := settlement.New(f.pool).ReverseUnappliedPayment(ctx, f.actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: uuid.NewString()}, ID: payment.ID, CustomerID: f.customer, EffectiveDate: "2020-01-02", Reason: "error"})
	financeCheck(t, err)
	grants := financeGrants(authorization.Grant{Permission: "settlement.view", Scope: authorization.All})
	reader := financeReader(t, f, "Active history only", grants)
	archivedReader := financeReader(t, f, "Archived history reader", append(grants, authorization.Grant{Permission: "customer.archived.view", Scope: authorization.All}))
	before, err := services.ReadFinancialReport(ctx, f.pool, reader, authorization.FinancialSettlement, services.FinancialReportFilter{CustomerID: f.customer})
	financeCheck(t, err)
	activeRef := financeEvent(t, before.Activity, "payment", payment.ID.String()).Reference
	financeEvent(t, before.Activity, "payment_reversal", reversal.ID.String())
	preview, err := f.svc.PreviewCustomerArchive(ctx, f.actor, f.customer)
	financeCheck(t, err)
	if len(preview.Blockers) != 0 {
		t.Fatalf("unexpected archive blockers: %+v", preview.Blockers)
	}
	financeCheck(t, f.svc.ArchiveCustomer(ctx, f.actor, f.customer, preview.Snapshot))
	financeDenied(t, financeReferenceCheck(f, reader, activeRef))
	financeCheck(t, financeReferenceCheck(f, archivedReader, activeRef))
	ordinary, err := services.ReadFinancialReport(ctx, f.pool, reader, authorization.FinancialSettlement, services.FinancialReportFilter{})
	financeCheck(t, err)
	if len(ordinary.Activity) != 0 || ordinary.Totals.NetPaymentsCents != 0 {
		t.Fatal("archived history appeared as default current activity")
	}
	_, err = services.ReadFinancialReport(ctx, f.pool, reader, authorization.FinancialSettlement, services.FinancialReportFilter{IncludeArchived: true})
	financeDenied(t, err)
	history, err := services.ReadFinancialReport(ctx, f.pool, archivedReader, authorization.FinancialSettlement, services.FinancialReportFilter{CustomerID: f.customer, IncludeArchived: true})
	financeCheck(t, err)
	archivedEvent := financeEvent(t, history.Activity, "payment", payment.ID.String())
	financeEvent(t, history.Activity, "payment_reversal", reversal.ID.String())
	if len(history.Activity) != len(before.Activity) || history.Totals != before.Totals || !archivedEvent.Archived || !archivedEvent.Reference.Archived {
		t.Fatalf("archiving altered immutable history: %+v", history)
	}
	var archived bool
	financeCheck(t, f.pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM customers WHERE id=$1`, f.customer).Scan(&archived))
	if !archived {
		t.Fatal("report reopened Customer")
	}
	financeCheck(t, f.svc.CustomerLifecycle(ctx, f.actor, f.customer, true))
	financeCheck(t, financeReferenceCheck(f, reader, activeRef))
	financeDenied(t, financeReferenceCheck(f, reader, archivedEvent.Reference)) // stored archived facet survives restoration
	financeCheck(t, financeReferenceCheck(f, archivedReader, archivedEvent.Reference))
	after, err := services.ReadFinancialReport(ctx, f.pool, reader, authorization.FinancialSettlement, services.FinancialReportFilter{CustomerID: f.customer})
	financeCheck(t, err)
	if len(after.Activity) != len(before.Activity) || after.Totals != before.Totals {
		t.Fatal("restore created or erased financial activity")
	}
	for _, e := range before.Activity {
		retained := financeEvent(t, after.Activity, e.Kind, e.ID)
		if retained.AmountCents != e.AmountCents || retained.NetPaymentsCents != e.NetPaymentsCents || retained.RecordedAt == nil || e.RecordedAt == nil || !retained.RecordedAt.Equal(*e.RecordedAt) {
			t.Fatal("restore rewrote an immutable event")
		}
	}
}

func TestFinancialReportHiddenInvoiceHistoryRetainsOnlySourceIdentity(t *testing.T) {
	f := newCommercialReportFixture(t)
	f.actor.Channel = "web"
	ctx := financeContext(f)
	const secret = "Hidden source Invoice title must not be enriched"
	estimate := f.estimate(t, 0, secret, time.Now())
	_, err := f.pool.Exec(ctx, `UPDATE estimates SET line_items='[{"title":"Work","quantity":1,"unit_price":100}]'::jsonb WHERE id=$1`, estimate)
	financeCheck(t, err)
	conv := conversion.New(f.pool)
	ca := conversion.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}
	cycle, err := conv.Convert(context.Background(), ca, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: estimate})
	financeCheck(t, err)
	financeCheck(t, statusflow.New(f.pool).FinalizeInvoice(context.Background(), statusflow.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}, cycle.InvoiceID))
	svc := settlement.New(f.pool)
	payment, err := svc.RecordInvoicePaymentWithCredit(ctx, f.actor, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: uuid.NewString()}, InvoiceID: cycle.InvoiceID, AmountCents: 12500, Method: settlement.Cash, ReceivedDate: "2020-01-01"})
	financeCheck(t, err)
	reader := financeReader(t, f, "History without Invoice View", financeGrants(authorization.Grant{Permission: "settlement.view", Scope: authorization.All}))
	before, err := services.ReadFinancialReport(ctx, f.pool, reader, authorization.FinancialSettlement, services.FinancialReportFilter{})
	financeCheck(t, err)
	ref := financeEvent(t, before.Activity, "payment", payment.ID.String()).Reference
	financeDenied(t, f.svc.ParentAccess(ctx, reader, "invoice", cycle.InvoiceID, ""))
	// The installed database lifecycle guard permits this paid Invoice archive.
	// Ordinary source visibility does not control Customer settlement provenance.
	_, err = f.pool.Exec(ctx, `UPDATE invoices SET deleted_at=clock_timestamp() WHERE id=$1`, cycle.InvoiceID)
	financeCheck(t, err)
	archivedHistory, err := services.ReadFinancialReport(ctx, f.pool, reader, authorization.FinancialSettlement, services.FinancialReportFilter{})
	financeCheck(t, err)
	if !reflect.DeepEqual(before.Activity, archivedHistory.Activity) || before.Totals != archivedHistory.Totals {
		t.Fatal("archived source Invoice altered authorized settlement history")
	}
	financeCheck(t, financeReferenceCheck(f, reader, ref))
	_, err = f.pool.Exec(ctx, `UPDATE invoices SET deleted_at=NULL WHERE id=$1`, cycle.InvoiceID)
	financeCheck(t, err)
	reversal, err := svc.ReversePayment(ctx, settlement.Actor{ID: f.actor.UserID, CompanyID: f.company, Role: "admin"}, settlement.ReverseRequest{Operation: settlement.Operation{Key: uuid.NewString()}, ID: payment.ID, InvoiceID: cycle.InvoiceID, EffectiveDate: "2020-01-02", Reason: "error"})
	financeCheck(t, err)
	_, err = conv.Revert(context.Background(), ca, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: cycle.InvoiceID})
	financeCheck(t, err)
	financeDenied(t, f.svc.ParentAccess(ctx, f.actor, "invoice", cycle.InvoiceID, "")) // hidden even from ordinary administrator navigation
	financeCheck(t, financeReferenceCheck(f, reader, ref))
	history, err := services.ReadFinancialReport(ctx, f.pool, reader, authorization.FinancialSettlement, services.FinancialReportFilter{})
	financeCheck(t, err)
	original := financeEvent(t, history.Activity, "payment", payment.ID.String())
	compensating := financeEvent(t, history.Activity, "payment_reversal", reversal.ID.String())
	if original.InvoiceID != cycle.InvoiceID || compensating.InvoiceID != cycle.InvoiceID || compensating.OriginalID != payment.ID.String() || history.Totals.NetPaymentsCents != 0 || len(history.Activity) != 3 {
		t.Fatalf("hidden source erased or netted history: %+v", history)
	}
	for _, e := range history.Activity {
		financeCheck(t, financeReferenceCheck(f, reader, e.Reference))
	}
	encoded, err := json.Marshal(history)
	financeCheck(t, err)
	if strings.Contains(string(encoded), secret) {
		t.Fatal("source Invoice title enriched without authority")
	}
}

func TestFinancialReportReferencesRejectWrongCustomerCompanyAndSource(t *testing.T) {
	f := newCommercialReportFixture(t)
	ctx := context.Background()
	payment := financeReceipt(t, f, 101, "2020-01-01")
	invoice := financeInvoice(t, f, 1, "invoiced", "", 200, 0)
	combined := financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{})
	eventRef := financeEvent(t, combined.Activity, "payment", payment.ID.String()).Reference
	if len(combined.Credits) != 1 {
		t.Fatal("missing Credit source")
	}
	creditRef := combined.Credits[0].Reference
	aging := financeReport(t, f, authorization.FinancialAging, services.FinancialReportFilter{})
	if len(aging.Invoices) != 1 || aging.Invoices[0].ID != invoice {
		t.Fatal("missing Invoice source")
	}
	invoiceRef := aging.Invoices[0].Reference
	other := f
	financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Other source owner') RETURNING id`, f.company).Scan(&other.customer))
	otherPayment := financeReceipt(t, other, 303, "2020-01-01")
	var otherCredit string
	financeCheck(t, f.pool.QueryRow(ctx, `SELECT id::text FROM customer_credits WHERE source_payment_id=$1`, otherPayment.ID).Scan(&otherCredit))
	otherInvoice := financeInvoice(t, other, 2, "invoiced", "", 404, 0)
	for _, ref := range []authorization.FinancialReference{eventRef, creditRef, invoiceRef} {
		financeCheck(t, financeReferenceCheck(f, f.actor, ref))
		wrong := ref
		wrong.CustomerID = other.customer
		financeDenied(t, financeReferenceCheck(f, f.actor, wrong))
	}
	wrong := eventRef
	wrong.EventID = otherPayment.ID.String()
	financeDenied(t, financeReferenceCheck(f, f.actor, wrong))
	wrong = creditRef
	wrong.CreditID = otherCredit
	financeDenied(t, financeReferenceCheck(f, f.actor, wrong))
	wrong = invoiceRef
	wrong.Invoices = []authorization.FinancialInvoiceReference{{ID: otherInvoice}}
	financeDenied(t, financeReferenceCheck(f, f.actor, wrong))
	wrong = eventRef
	wrong.EventKind = "credit_creation"
	financeDenied(t, financeReferenceCheck(f, f.actor, wrong))
	wrong = eventRef
	wrong.EventID = uuid.NewString()
	financeDenied(t, financeReferenceCheck(f, f.actor, wrong))
	var company, customer int64
	financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO companies(name,slug) VALUES('Other company','financial-reference-other') RETURNING id`).Scan(&company))
	financeCheck(t, f.pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Foreign source Customer') RETURNING id`, company).Scan(&customer))
	foreignPayment, foreignCredit := uuid.NewString(), uuid.NewString()
	_, err := f.pool.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,amount_cents,method,received_date) VALUES($1,$2,$3,505,'cash','2020-01-01');`, foreignPayment, company, customer)
	financeCheck(t, err)
	_, err = f.pool.Exec(ctx, `INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date) VALUES($1,$2,$3,$4,505,'2020-01-01')`, foreignCredit, company, customer, foreignPayment)
	financeCheck(t, err)
	for _, ref := range []authorization.FinancialReference{eventRef, creditRef, invoiceRef} {
		wrong := ref
		wrong.CustomerID = customer
		financeDenied(t, financeReferenceCheck(f, f.actor, wrong))
	}
	wrong = eventRef
	wrong.EventID = foreignPayment
	financeDenied(t, financeReferenceCheck(f, f.actor, wrong))
	wrong = creditRef
	wrong.CreditID = foreignCredit
	financeDenied(t, financeReferenceCheck(f, f.actor, wrong))
	_, err = services.ReadFinancialReport(ctx, f.pool, f.actor, authorization.FinancialCombined, services.FinancialReportFilter{CustomerID: customer})
	financeDenied(t, err)
	visible := financeReport(t, f, authorization.FinancialCombined, services.FinancialReportFilter{})
	for _, e := range visible.Activity {
		if e.CustomerID == customer || e.ID == foreignPayment || e.PaymentID == foreignPayment {
			t.Fatal("foreign source entered rows or totals")
		}
	}
	if visible.Totals.NetPaymentsCents != 404 || visible.Totals.AvailableCreditCents != 404 {
		t.Fatalf("foreign source changed totals: %+v", visible.Totals)
	}
}
