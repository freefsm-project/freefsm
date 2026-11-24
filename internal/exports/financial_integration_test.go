package exports

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/conversion"
	"github.com/freefsm-project/freefsm/internal/settlement"
	"github.com/freefsm-project/freefsm/internal/statusflow"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func financialExportReady(t *testing.T, pool *pgxpool.Pool, actor authorization.Actor, kind string, options json.RawMessage) (int64, [][]string) {
	t.Helper()
	ctx := context.Background()
	s := Product(pool)
	op := uuid.NewString()
	id, err := s.Accept(ctx, actor, op, kind, options)
	if err != nil {
		t.Fatal(err)
	}
	if replay, err := s.Accept(ctx, actor, op, kind, options); err != nil || replay != id {
		t.Fatalf("intent replay %d: %v", replay, err)
	}
	if processed, err := s.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("generation processed=%v: %v", processed, err)
	}
	// No in-memory result survives this reconstruction.
	s = Product(pool)
	status, err := s.Status(ctx, actor, id)
	if err != nil || status.State != "Ready" || status.Blocked || status.AsOf == nil {
		t.Fatalf("status %+v: %v", status, err)
	}
	d, err := s.PrepareDownload(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(d).ReadAll()
	_ = d.Close()
	if err != nil {
		t.Fatal(err)
	}
	if status.RowCount == nil || *status.RowCount != int64(len(rows)-1) {
		t.Fatal("durable row count differs from artifact")
	}
	for _, row := range rows[1:] {
		at, err := time.Parse(time.RFC3339Nano, row[0])
		if err != nil || !at.Equal(*status.AsOf) {
			t.Fatalf("row/receipt snapshot mismatch: %q %+v", row[0], status.AsOf)
		}
	}
	selection, err := s.RecoverySelection(ctx, actor, id)
	if err != nil || selection.Kind != kind {
		t.Fatalf("recovery %s: %+v %v", kind, selection, err)
	}
	return id, rows
}

func financialExportReader(t *testing.T, pool *pgxpool.Pool, admin authorization.Actor, company int64, permissions ...string) (authorization.Actor, int64) {
	t.Helper()
	ctx := context.Background()
	var user int64
	if err := pool.QueryRow(ctx, `INSERT INTO users(company_id,email,name,password_hash,role) VALUES($1,$2,'Finance reader','hash','tech') RETURNING id`, company, uuid.NewString()+"@export99.test").Scan(&user); err != nil {
		t.Fatal(err)
	}
	var grants []authorization.Grant
	for _, permission := range permissions {
		grants = append(grants, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	auth := authorization.New(pool)
	role, err := auth.SaveRole(ctx, admin, 0, "Finance "+uuid.NewString(), grants)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.AssignUser(ctx, admin, user, role); err != nil {
		t.Fatal(err)
	}
	return authorization.Actor{UserID: user, Channel: "web"}, role
}

func TestFinancialExportDurableFiveFacetsHistoryAndUnknown(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	admin.Channel = "web"
	ctx := context.Background()
	var customer, invoice int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,$2) RETURNING id`, company, "=Customer, \"quoted\"\nsecond line").Scan(&customer); err != nil {
		t.Fatal(err)
	}
	var estimate int64
	if err := pool.QueryRow(ctx, `INSERT INTO estimates(company_id,customer_id,title,line_items,tax_rate) VALUES($1,$2,'Original receivable','[{"title":"Work","unit_price":100,"quantity":1}]','0') RETURNING id`, company, customer).Scan(&estimate); err != nil {
		t.Fatal(err)
	}
	conversions := conversion.New(pool)
	conversionActor := conversion.Actor{ID: admin.UserID, CompanyID: company, Role: "admin"}
	converted, err := conversions.Convert(ctx, conversionActor, conversion.ConvertRequest{Operation: conversion.Operation{Key: uuid.New()}, EstimateID: estimate})
	if err != nil {
		t.Fatal(err)
	}
	invoice = converted.InvoiceID
	if err := statusflow.New(pool).FinalizeInvoice(ctx, statusflow.Actor{ID: admin.UserID, CompanyID: company, Role: "admin"}, invoice); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO invoices(company_id,customer_id,invoice_number,title,invoice_date,due_date,line_items,tax_rate,status_id) SELECT $1,$2,n,'Receivable',CURRENT_DATE,CURRENT_DATE,'[{"title":"Work","unit_price":100,"quantity":1}]','0',(SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced') FROM generate_series(2,105) n`, company, customer); err != nil {
		t.Fatal(err)
	}
	settle := settlement.New(pool)
	date := settlement.Date("2020-01-03")
	payment, err := settle.RecordInvoicePaymentWithCredit(ctx, admin, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: uuid.NewString()}, InvoiceID: invoice, AmountCents: 5000, Method: settlement.Cash, ReceivedDate: date})
	if err != nil {
		t.Fatal(err)
	}
	for _, amount := range []int64{10000, 4000} {
		if _, err := settle.RecordCustomerPayment(ctx, admin, customer, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: uuid.NewString()}, AmountCents: amount, Method: settlement.Cash, ReceivedDate: date}); err != nil {
			t.Fatal(err)
		}
	}
	installed := authorization.WithActor(ctx, authorization.New(pool), admin)
	actor := settlement.Actor{ID: admin.UserID, CompanyID: company}
	var credit uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM customer_credits WHERE customer_id=$1 ORDER BY source_date,id LIMIT 1`, customer).Scan(&credit); err != nil {
		t.Fatal(err)
	}
	application, err := settle.ApplyCredit(installed, actor, settlement.ApplyCreditRequest{Operation: settlement.Operation{Key: uuid.NewString()}, InvoiceID: invoice, CreditID: credit, RequestedCents: 1000, EffectiveDate: date})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settle.ReverseCreditApplication(installed, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: uuid.NewString()}, ID: application.ID, InvoiceID: invoice, CustomerID: customer, EffectiveDate: date, Reason: "Wrong allocation"}); err != nil {
		t.Fatal(err)
	}
	refund, err := settle.RefundCredit(installed, actor, settlement.RefundCreditRequest{Operation: settlement.Operation{Key: uuid.NewString()}, CustomerID: customer, AmountCents: 12000, Method: settlement.Cash, EffectiveDate: date, Reason: "Actual external return"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := settle.ReverseRefund(installed, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: uuid.NewString()}, ID: refund.ID, CustomerID: customer, EffectiveDate: date, Reason: "Return failed"}); err != nil {
		t.Fatal(err)
	}
	// Preservation fixture: genuine unknown entry evidence, not an UPDATE of an
	// immutable modern receipt, and no fabricated migration timestamp.
	legacyPayment, legacyCredit := uuid.NewString(), uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO invoice_payments(id,company_id,customer_id,invoice_id,amount_cents,method,received_date,actor_id,created_at) VALUES($1,$2,$3,NULL,700,'cash','2020-01-01',NULL,NULL)`, legacyPayment, company, customer); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO customer_credits(id,company_id,customer_id,source_payment_id,original_amount_cents,source_date) VALUES($1,$2,$3,$4,700,'2020-01-01')`, legacyCredit, company, customer, legacyPayment); err != nil {
		t.Fatal(err)
	}
	options := json.RawMessage(fmt.Sprintf(`{"customer_id":%d}`, customer))
	ids := map[string]int64{}
	for _, kind := range []string{InvoiceAgingKind, CustomerFinancialsKind, SettlementActivityKind, CurrentCreditKind, PaymentsCreditsKind} {
		id, rows := financialExportReady(t, pool, admin, kind, options)
		ids[kind] = id
		switch kind {
		case InvoiceAgingKind:
			if len(rows) != 113 || rows[len(rows)-1][9] != "105" || rows[len(rows)-1][12] != "10450.00" {
				t.Fatalf("all matching Invoice totals: %d %v", len(rows), rows[len(rows)-1])
			}
			if !strings.HasPrefix(rows[1][4], "'=Customer,") {
				t.Fatal("formula/CSV quoting lost")
			}
		case CustomerFinancialsKind:
			if len(rows) != 3 || rows[1][5] != "105" || rows[1][7] != "50.00" || rows[1][8] != "10450.00" {
				t.Fatalf("Customer summary: %v", rows)
			}
		case SettlementActivityKind, PaymentsCreditsKind:
			refundChildren := 0
			for _, r := range rows[1:] {
				if r[2] == "allocation" && r[20] == refund.ID.String() {
					refundChildren++
					if r[23] != "" || r[24] != "" || r[17] == "" || r[18] == "" {
						t.Fatalf("multiplied/ref-less refund: %v", r)
					}
				}
				if r[2] == "net_payments_total" {
					want := "190.00"
					if r[3] == "unknown_entry_time" {
						want = "7.00"
					}
					if r[24] != want {
						t.Fatalf("wrong signed net: %v", r)
					}
				}
				if r[2] == "event" && r[10] == legacyPayment && (r[14] != "" || r[15] != "false" || r[3] != "unknown_entry_time") {
					t.Fatal("unknown receipt attributed to an invented entry date")
				}
			}
			if refundChildren != 4 {
				t.Fatalf("expected two persisted sources original+reversal, got %d", refundChildren)
			}
		case CurrentCreditKind:
			if rows[len(rows)-1][12] != "147.00" {
				t.Fatalf("current balance: %v", rows)
			}
			for _, r := range rows[1:] {
				if r[6] == legacyCredit && (r[9] != "" || r[10] != "false") {
					t.Fatal("Credit used migration insertion timestamp")
				}
			}
		}
	}
	// A bounded empty dated period still discloses unknowns and current Credit.
	_, bounded := financialExportReady(t, pool, admin, PaymentsCreditsKind, json.RawMessage(fmt.Sprintf(`{"customer_id":%d,"from":"2000-01-01","to":"2000-01-02"}`, customer)))
	for _, r := range bounded[1:] {
		if r[2] == "net_payments_total" && r[3] == "period" && r[24] != "0.00" {
			t.Fatal("backdated effective dates leaked into entry-time selection")
		}
	}
	if bounded[len(bounded)-1][29] != "147.00" {
		t.Fatal("period filtered current Credit")
	}
	// Nonempty independent exports exercise source rechecks without lending
	// Invoice Export or the other financial facet to the requester.
	for _, test := range []struct {
		kind   string
		grants []string
	}{
		{SettlementActivityKind, []string{"settlement.view", "settlement.export"}},
		{CurrentCreditKind, []string{"credit.view", "credit.export"}},
		{PaymentsCreditsKind, []string{"settlement.view", "settlement.export", "credit.view", "credit.export"}},
	} {
		permissions := append([]string{"customer.view", "customer.financial.view"}, test.grants...)
		reader, _ := financialExportReader(t, pool, admin, company, permissions...)
		financialExportReady(t, pool, reader, test.kind, options)
	}
	// Mutate only a stored SUMMARY reference, leaving CSV bytes/digest and all
	// detail references valid. Recheck must reject ownership/allocation changes
	// before releasing any bytes, rather than relying on an adjacent detail row.
	var otherCustomer int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Other financial owner') RETURNING id`, company).Scan(&otherCustomer); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{CurrentCreditKind, SettlementActivityKind} {
		id, _ := financialExportReady(t, pool, admin, kind, options)
		var raw json.RawMessage
		var ordinal int64
		if err := pool.QueryRow(ctx, `SELECT ordinal,reference FROM export_records WHERE export_id=$1 ORDER BY ordinal DESC LIMIT 1`, id).Scan(&ordinal, &raw); err != nil {
			t.Fatal(err)
		}
		var ref financialExportReference
		if err := json.Unmarshal(raw, &ref); err != nil {
			t.Fatal(err)
		}
		if !ref.Summary || len(ref.Provenance) == 0 {
			t.Fatal("summary omitted persisted source provenance")
		}
		if kind == CurrentCreditKind {
			ref.Provenance[0].CustomerID = otherCustomer
		} else {
			changed := false
			for i := range ref.Provenance {
				if ref.Provenance[i].Kind == "credit_refund" && len(ref.Provenance[i].Allocations) > 0 {
					ref.Provenance[i].Allocations[0].CreditID = legacyCredit
					ref.Provenance[i].Allocations[0].PaymentID = legacyPayment
					changed = true
					break
				}
			}
			if !changed {
				t.Fatal("missing refund allocation summary contributor")
			}
		}
		raw, err = json.Marshal(ref)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE export_records SET reference=$1 WHERE export_id=$2 AND ordinal=$3`, raw, id, ordinal); err != nil {
			t.Fatal(err)
		}
		d, err := Product(pool).PrepareDownload(ctx, admin, id)
		if !errors.Is(err, ErrUnavailable) || d != nil {
			if d != nil {
				_ = d.Close()
			}
			t.Fatalf("tampered %s summary released bytes: %v", kind, err)
		}
		status, err := Product(pool).Status(ctx, admin, id)
		if err != nil || !status.Blocked || status.RowCount != nil || status.AsOf != nil {
			t.Fatalf("blocked metadata leak: %+v %v", status, err)
		}
		if selection, err := Product(pool).RecoverySelection(ctx, admin, id); !errors.Is(err, ErrUnavailable) || selection.Kind != "" || len(selection.Options) != 0 {
			t.Fatalf("tampered summary recovery leak: %+v %v", selection, err)
		}
	}
	// Complete the partial Invoice receipt unwind and hide its source Invoice;
	// historical IDs remain provenance rather than ordinary Invoice links.
	if _, err := settle.ReversePayment(installed, actor, settlement.ReverseRequest{Operation: settlement.Operation{Key: uuid.NewString()}, ID: payment.ID, InvoiceID: invoice, CustomerID: customer, EffectiveDate: date, Reason: "external_return"}); err != nil {
		t.Fatal(err)
	}
	if _, err := conversions.Revert(ctx, conversionActor, conversion.RevertRequest{Operation: conversion.Operation{Key: uuid.New()}, InvoiceID: invoice}); err != nil {
		t.Fatal(err)
	}
	_, unwound := financialExportReady(t, pool, admin, SettlementActivityKind, options)
	seenReversal := false
	for _, row := range unwound[1:] {
		if row[2] == "event" && row[9] == "payment_reversal" && row[12] == payment.ID.String() {
			seenReversal = true
		}
		if row[2] == "net_payments_total" && row[3] == "dated_history" && row[24] != "140.00" {
			t.Fatalf("receipt reversal did not reconcile: %v", row)
		}
	}
	if !seenReversal {
		t.Fatal("hidden Invoice erased receipt reversal")
	}
	if d, err := Product(pool).PrepareDownload(ctx, admin, ids[SettlementActivityKind]); err != nil {
		t.Fatalf("historical Invoice wrongly required ordinary View: %v", err)
	} else {
		_ = d.Close()
	}
	if d, err := Product(pool).PrepareDownload(ctx, admin, ids[CustomerFinancialsKind]); !errors.Is(err, ErrUnavailable) || d != nil {
		if d != nil {
			_ = d.Close()
		}
		t.Fatalf("summary contributor loss did not block whole artifact: %v", err)
	}
}

func TestFinancialExportIndependentGrantsEmptyRevocationAndRecovery(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	ctx := context.Background()
	base := []string{"customer.view", "customer.financial.view"}
	cases := []struct {
		name    string
		extra   []string
		allowed map[string]bool
	}{
		{"financial", []string{"customer.financial.export", "invoice.view"}, map[string]bool{InvoiceAgingKind: true, CustomerFinancialsKind: true}},
		{"settlement", []string{"settlement.view", "settlement.export"}, map[string]bool{SettlementActivityKind: true}},
		{"credit", []string{"credit.view", "credit.export"}, map[string]bool{CurrentCreditKind: true}},
		{"combined", []string{"settlement.view", "settlement.export", "credit.view", "credit.export"}, map[string]bool{SettlementActivityKind: true, CurrentCreditKind: true, PaymentsCreditsKind: true}},
		{"operational", []string{"customer.export"}, map[string]bool{}},
		{"financial without Invoice View", []string{"customer.financial.export"}, map[string]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			permissions := append(append([]string{}, base...), tc.extra...)
			reader, role := financialExportReader(t, pool, admin, company, permissions...)
			var ready int64
			for _, kind := range []string{InvoiceAgingKind, CustomerFinancialsKind, SettlementActivityKind, CurrentCreditKind, PaymentsCreditsKind} {
				if tc.allowed[kind] {
					ready, _ = financialExportReady(t, pool, reader, kind, json.RawMessage(`{}`))
					continue
				}
				if _, err := Product(pool).Accept(ctx, reader, uuid.NewString(), kind, json.RawMessage(`{}`)); !errors.Is(err, authorization.ErrForbidden) {
					t.Fatalf("%s escaped independent grants: %v", kind, err)
				}
			}
			if ready == 0 {
				return
			}
			if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '25 hours' WHERE id=$1`, ready); err != nil {
				t.Fatal(err)
			}
			if _, err := Product(pool).RecoverySelection(ctx, reader, ready); err != nil {
				t.Fatal(err)
			}
			if d, err := Product(pool).PrepareDownload(ctx, reader, ready); !errors.Is(err, ErrUnavailable) || d != nil {
				t.Fatal("expired bytes released")
			}
			if _, err := Product(pool).RecoverySelection(ctx, admin, ready); !errors.Is(err, authorization.ErrNotFound) {
				t.Fatalf("requester isolation: %v", err)
			}
			// SaveRole fences actual catalog grants; empties still lose recovery.
			var rev int64
			var name string
			if err := pool.QueryRow(ctx, `SELECT revision,name FROM authorization_roles WHERE id=$1`, role).Scan(&rev, &name); err != nil {
				t.Fatal(err)
			}
			if _, err := authorization.New(pool).SaveRole(ctx, admin, role, name, nil, rev); err != nil {
				t.Fatal(err)
			}
			selection, err := Product(pool).RecoverySelection(ctx, reader, ready)
			if !errors.Is(err, ErrUnavailable) || selection.Kind != "" || len(selection.Options) != 0 {
				t.Fatalf("revoked empty leaked recovery: %+v %v", selection, err)
			}
		})
	}
}

func TestFinancialExportAtomicFailureAndGenerationRevocation(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	ctx := context.Background()
	reader, role := financialExportReader(t, pool, admin, company, "customer.view", "customer.financial.view", "settlement.view", "settlement.export")
	s := Product(pool)
	id, err := s.Accept(ctx, reader, uuid.NewString(), SettlementActivityKind, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_financial_export99() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal=2 THEN RAISE EXCEPTION 'injected export storage failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_financial_export99 BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION fail_financial_export99()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessOne(ctx); err == nil {
		t.Fatal("injected failure published artifact")
	}
	var records int
	var state string
	if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1) FROM export_jobs WHERE id=$1`, id).Scan(&state, &records); err != nil {
		t.Fatal(err)
	}
	if state != "Failed" || records != 0 {
		t.Fatalf("partial publication: %s %d", state, records)
	}
	if _, err := s.RecoverySelection(ctx, reader, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_financial_export99 ON export_records`); err != nil {
		t.Fatal(err)
	}
	second, err := s.Accept(ctx, reader, uuid.NewString(), SettlementActivityKind, json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var rev int64
	var name string
	if err := pool.QueryRow(ctx, `SELECT revision,name FROM authorization_roles WHERE id=$1`, role).Scan(&rev, &name); err != nil {
		t.Fatal(err)
	}
	if _, err := authorization.New(pool).SaveRole(ctx, admin, role, name, nil, rev); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProcessOne(ctx); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("generation used accepted stale grant: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1) FROM export_jobs WHERE id=$1`, second).Scan(&state, &records); err != nil {
		t.Fatal(err)
	}
	if state != "Failed" || records != 0 {
		t.Fatalf("revoked generation published: %s %d", state, records)
	}
}

func TestFinancialExportAssignedSummaryContributorRecovery(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	ctx := context.Background()
	reader, role := financialExportReader(t, pool, admin, company, "customer.view", "customer.financial.view", "customer.financial.export", "invoice.view")
	var revision, customer, job int64
	var name string
	if err := pool.QueryRow(ctx, `SELECT revision,name FROM authorization_roles WHERE id=$1`, role).Scan(&revision, &name); err != nil {
		t.Fatal(err)
	}
	grants := []authorization.Grant{{Permission: "customer.view", Scope: authorization.All}, {Permission: "customer.financial.view", Scope: authorization.All}, {Permission: "customer.financial.export", Scope: authorization.All}, {Permission: "invoice.view", Scope: authorization.Assigned}}
	if _, err := authorization.New(pool).SaveRole(ctx, admin, role, name, grants, revision); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Assigned finance') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO jobs(company_id,customer_id,job_type) VALUES($1,$2,'Assigned finance Job') RETURNING id`, company, customer).Scan(&job); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_assignments(job_id,user_id) VALUES($1,$2)`, job, reader.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO invoices(company_id,customer_id,job_id,title,line_items,tax_rate,status_id) VALUES($1,$2,$3,'Assigned amount','[{"title":"Work","unit_price":100,"quantity":1}]','0',(SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced')),($1,$2,NULL,'Inaccessible amount','[{"title":"Work","unit_price":999,"quantity":1}]','0',(SELECT id FROM statuses WHERE company_id=$1 AND category_key='invoice:invoiced'))`, company, customer, job); err != nil {
		t.Fatal(err)
	}
	id, rows := financialExportReady(t, pool, reader, CustomerFinancialsKind, json.RawMessage(fmt.Sprintf(`{"customer_id":%d}`, customer)))
	if len(rows) != 3 || rows[1][5] != "1" || rows[1][8] != "100.00" {
		t.Fatalf("J contributor scope widened: %v", rows)
	}
	if _, err := pool.Exec(ctx, `UPDATE export_jobs SET ready_at=clock_timestamp()-interval '25 hours' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := Product(pool).RecoverySelection(ctx, reader, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2`, job, reader.UserID); err != nil {
		t.Fatal(err)
	}
	selection, err := Product(pool).RecoverySelection(ctx, reader, id)
	if !errors.Is(err, ErrUnavailable) || selection.Kind != "" || len(selection.Options) != 0 {
		t.Fatalf("summary-only contributor lost but filters disclosed: %+v %v", selection, err)
	}
}

func TestFinancialExportCombinedSelectiveCreditExportRevocation(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	admin.Channel = "web"
	ctx := context.Background()
	var customer int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Selective combined authority') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := settlement.New(pool).RecordCustomerPayment(ctx, admin, customer, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: uuid.NewString()}, AmountCents: 10000, Method: settlement.Cash, ReceivedDate: "2020-01-01"}); err != nil {
		t.Fatal(err)
	}
	permissions := []string{"customer.view", "customer.financial.view", "settlement.view", "settlement.export", "credit.view", "credit.export"}
	reader, role := financialExportReader(t, pool, admin, company, permissions...)
	options := json.RawMessage(fmt.Sprintf(`{"customer_id":%d}`, customer))
	id, rows := financialExportReady(t, pool, reader, PaymentsCreditsKind, options)
	if rows[len(rows)-1][29] != "100.00" {
		t.Fatal("fixture must contain current Credit as well as settlement events")
	}
	var revision int64
	var name string
	if err := pool.QueryRow(ctx, `SELECT revision,name FROM authorization_roles WHERE id=$1`, role).Scan(&revision, &name); err != nil {
		t.Fatal(err)
	}
	var retained []authorization.Grant
	for _, permission := range permissions[:len(permissions)-1] {
		retained = append(retained, authorization.Grant{Permission: permission, Scope: authorization.All})
	}
	if _, err := authorization.New(pool).SaveRole(ctx, admin, role, name, retained, revision); err != nil {
		t.Fatal(err)
	}
	p, err := authorization.New(pool).Principal(ctx, reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, permission := range permissions[:len(permissions)-1] {
		if !p.Allows(permission, authorization.All) {
			t.Fatalf("unexpected loss of retained grant %s", permission)
		}
	}
	if p.Allows("credit.export", authorization.All) {
		t.Fatal("Credit Export was not revoked")
	}
	s := Product(pool)
	status, err := s.Status(ctx, reader, id)
	if err != nil || !status.Blocked || status.State != "Ready" || status.RowCount != nil || status.AsOf != nil || status.ReadyAt != nil {
		t.Fatalf("selective revocation leaked status metadata: %+v %v", status, err)
	}
	if download, err := s.PrepareDownload(ctx, reader, id); !errors.Is(err, ErrUnavailable) || download != nil {
		if download != nil {
			_ = download.Close()
		}
		t.Fatalf("selective revocation released combined bytes: %v", err)
	}
	selection, err := s.RecoverySelection(ctx, reader, id)
	if !errors.Is(err, ErrUnavailable) || selection.Kind != "" || len(selection.Options) != 0 {
		t.Fatalf("selective revocation leaked recovery: %+v %v", selection, err)
	}
	if _, err := s.Accept(ctx, reader, uuid.NewString(), PaymentsCreditsKind, options); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("combined acceptance borrowed settlement Export: %v", err)
	}
	// A genuinely independent settlement export remains usable with the exact
	// same financial history after only Credit Export has been removed.
	_, settlementRows := financialExportReady(t, pool, reader, SettlementActivityKind, options)
	if len(settlementRows) <= 17 {
		t.Fatal("independent settlement lost its nonempty events")
	}
}

func TestFinancialExportRunningWorkerInterruptionAndRecovery(t *testing.T) {
	pool, admin, company := operationalExportDB(t)
	admin.Channel = "web"
	ctx := context.Background()
	var customer int64
	if err := pool.QueryRow(ctx, `INSERT INTO customers(company_id,display_name) VALUES($1,'Interrupted financial export') RETURNING id`, company).Scan(&customer); err != nil {
		t.Fatal(err)
	}
	if _, err := settlement.New(pool).RecordCustomerPayment(ctx, admin, customer, settlement.RecordPaymentRequest{Operation: settlement.Operation{Key: uuid.NewString()}, AmountCents: 10000, Method: settlement.Cash, ReceivedDate: "2020-01-01"}); err != nil {
		t.Fatal(err)
	}
	s := Product(pool)
	id, err := s.Accept(ctx, admin, uuid.NewString(), PaymentsCreditsKind, json.RawMessage(fmt.Sprintf(`{"customer_id":%d}`, customer)))
	if err != nil {
		t.Fatal(err)
	}
	// A test-local database lock stops the actual engine after its header and
	// first detail insert, inside the real repeatable-read publication tx.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_financial_export99() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ordinal=2 THEN PERFORM pg_advisory_xact_lock(990099); END IF; RETURN NEW; END $$; CREATE TRIGGER pause_financial_export99 BEFORE INSERT ON export_records FOR EACH ROW EXECUTE FUNCTION pause_financial_export99()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_lock(990099)`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(990099)`) }()
	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		processed bool
		err       error
	}
	done := make(chan result, 1)
	go func() { processed, err := s.ProcessOne(workerContext); done <- result{processed, err} }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	witnessed := false
	for !witnessed {
		if err := pool.QueryRow(ctx, `SELECT state='Running' AND EXISTS(SELECT 1 FROM pg_locks l JOIN pg_database d ON d.oid=l.database WHERE l.locktype='advisory' AND l.classid=0 AND l.objid=990099 AND NOT l.granted AND d.datname=current_database()) FROM export_jobs WHERE id=$1`, id).Scan(&witnessed); err != nil {
			t.Fatal(err)
		}
		if witnessed {
			break
		}
		select {
		case early := <-done:
			t.Fatalf("worker ended before witnessed generation block: %+v", early)
		case <-deadline.C:
			t.Fatal("did not witness real Running worker waiting inside publication")
		case <-tick.C:
		}
	}
	t.Log("witnessed real Running worker blocked at ordinal 2 after header and first financial detail insert")
	cancel() // Exercise the installed worker's interruption path, not a state UPDATE.
	select {
	case stopped := <-done:
		if !stopped.processed || stopped.err == nil {
			t.Fatalf("interruption was not observed: %+v", stopped)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("interrupted worker did not stop")
	}
	var state string
	var count int
	if err := pool.QueryRow(ctx, `SELECT state,(SELECT count(*) FROM export_records WHERE export_id=$1) FROM export_jobs WHERE id=$1`, id).Scan(&state, &count); err != nil {
		t.Fatal(err)
	}
	if state != "Running" || count != 0 {
		t.Fatalf("interrupted publication not recoverable/atomic: state=%s rows=%d", state, count)
	}
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_unlock(990099)`); err != nil {
		t.Fatal(err)
	}
	// A fresh Service/worker must recover the committed Running receipt. No test
	// rewrites its state or emits records directly.
	restarted := Product(pool)
	if processed, err := restarted.ProcessOne(ctx); err != nil || !processed {
		t.Fatalf("restart did not recover Running work: %v %v", processed, err)
	}
	status, err := restarted.Status(ctx, admin, id)
	if err != nil || status.State != "Ready" || status.Blocked {
		t.Fatalf("recovered status: %+v %v", status, err)
	}
	download, err := restarted.PrepareDownload(ctx, admin, id)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(download).ReadAll()
	_ = download.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 21 || rows[len(rows)-1][29] != "100.00" {
		t.Fatalf("recovered artifact lost/duplicated financial rows: %d %v", len(rows), rows[len(rows)-1])
	}
	var generated int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authorization_audit WHERE target_type='export' AND target_id=$1 AND detail->>'event'='generated'`, id).Scan(&generated); err != nil {
		t.Fatal(err)
	}
	if generated != 1 {
		t.Fatalf("interrupted/recovered export audited generation %d times", generated)
	}
	if processed, err := restarted.ProcessOne(ctx); err != nil || processed {
		t.Fatalf("Ready job unexpectedly regenerated: %v %v", processed, err)
	}
	t.Log("fresh worker recovered original Running receipt; zero partial records; exactly one committed generation audit")
}
