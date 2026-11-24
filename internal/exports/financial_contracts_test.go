package exports

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
)

func TestFinancialCSVFixedContractsAndRegistry(t *testing.T) {
	doc, err := os.ReadFile("../../docs/v1-ticket-99-export-contracts.md")
	if err != nil {
		t.Fatal(err)
	}
	definitions := []Definition{InvoiceAging{}, CustomerFinancials{}, SettlementActivity{}, CurrentCredit{}, PaymentsCredits{}}
	wantKinds := []string{InvoiceAgingKind, CustomerFinancialsKind, SettlementActivityKind, CurrentCreditKind, PaymentsCreditsKind}
	wantWidths := []int{13, 15, 26, 13, 30}
	registry := Product(nil)
	for i, d := range definitions {
		if d.Kind() != wantKinds[i] || d.Scope() != authorization.All || len(d.Columns()) != wantWidths[i] {
			t.Fatalf("unexpected definition: %T", d)
		}
		if !strings.Contains(string(doc), "```text\n"+strings.Join(d.Columns(), ",")+"\n```") {
			t.Fatalf("%s header differs from fixed documented contract", d.Kind())
		}
		if reflect.TypeOf(registry.definitions[d.Kind()]) != reflect.TypeOf(d) {
			t.Fatalf("%s missing from Product", d.Kind())
		}
		if _, ok := d.(selectionAuthorizer); !ok {
			t.Fatalf("%s omits selected-input checks", d.Kind())
		}
		if err := d.Authorize(authorization.Principal{}, json.RawMessage(`{}`)); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("empty unauthorized export %s: %v", d.Kind(), err)
		}
	}
	combined := append([]string{}, SettlementActivity{}.Columns()...)
	for _, name := range (CurrentCredit{}).Columns() {
		found := false
		for _, existing := range combined {
			if existing == name {
				found = true
			}
		}
		if !found {
			combined = append(combined, name)
		}
	}
	if !reflect.DeepEqual(combined, (PaymentsCredits{}).Columns()) {
		t.Fatal("combined must be exact ordered facet union")
	}
}

func TestFinancialCSVStrictFilters(t *testing.T) {
	for _, raw := range []string{``, `null`, `[]`, `{}`, `{"sort":""}`} {
		_, err := ParseFinancialExportFilter(authorization.FinancialAging, json.RawMessage(raw))
		if (raw == `{}` || raw == `{"sort":""}`) != (err == nil) {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"customer_id":0}`, `{"customer_id":-1}`, `{"customer_id":1.5}`, `{"customer_id":9223372036854775808}`,
		`{"customer_id":"1"}`, `{"customer_id":null}`, `{"sort":null}`, `{"sort":{}}`,
		`{"customer_id":1,"customer_id":2}`, `{"sort":"customer_invoice","sort":"customer_invoice"}`,
		`{"page":1}`, `{"from":""}`, `{"include_archived":false}`, `{"bucket":"current"}`,
		`{"scope":"A"}`, `{"sort":"invoice_id"}`, `{} {}`, `{"customer_id":1`,
	} {
		if _, err := ParseFinancialExportFilter(authorization.FinancialAging, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid Aging intent %s", raw)
		}
	}
	for _, raw := range []string{`{"from":"2026-02-30"}`, `{"from":"2026-11-24","to":"2026-11-23"}`, `{"bucket":""}`, `{"include_archived":"true"}`} {
		if _, err := ParseFinancialExportFilter(authorization.FinancialCombined, json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid combined intent %s", raw)
		}
	}
	for _, kind := range []authorization.FinancialReportKind{authorization.FinancialAging, authorization.FinancialCustomers, authorization.FinancialSettlement, authorization.FinancialCredit, authorization.FinancialCombined} {
		f, err := ParseFinancialExportFilter(kind, json.RawMessage(`{"customer_id":12}`))
		if err != nil || f.CustomerID != 12 || f.Sort == "" {
			t.Fatalf("%s normalize: %+v %v", kind, f, err)
		}
	}
	f, err := ParseFinancialExportFilter(authorization.FinancialCombined, json.RawMessage(`{"from":"2026-11-23","to":"2026-11-23","include_archived":true}`))
	if err != nil || f.From != f.To || !f.IncludeArchived || f.Sort != "entry_asc" {
		t.Fatalf("valid bounded intent: %+v %v", f, err)
	}
}

func TestFinancialCSVExactMoney(t *testing.T) {
	for cents, want := range map[int64]string{0: "0.00", 1: "0.01", -1: "-0.01", 12345: "123.45", math.MaxInt64: "92233720368547758.07", math.MinInt64: "-92233720368547758.08"} {
		if got := financialMoney(cents); got != want {
			t.Fatalf("%d: %q != %q", cents, got, want)
		}
	}
	if safeCell(financialMoney(-1234)) != "'-12.34" {
		t.Fatal("negative money must retain engine spreadsheet escaping")
	}
	n := int64(math.MaxInt64)
	if err := financialSum(&n, 1); err == nil || n != math.MaxInt64 {
		t.Fatal("overflow must fail without changing total")
	}
}

func financialTestReport(kind authorization.FinancialReportKind) services.FinancialReport {
	return services.FinancialReport{Kind: kind, AsOf: time.Date(2026, 11, 23, 12, 0, 0, 123, time.UTC), Timezone: "America/New_York"}
}

func financialTestRecords(t *testing.T, report services.FinancialReport, width int) []Record {
	t.Helper()
	var records []Record
	err := emitFinancialReport(report, func(r Record) error {
		if len(r.Values) != width {
			t.Fatalf("%s row %s has width %d, want %d", report.Kind, r.Values[2], len(r.Values), width)
		}
		if r.Values[0] != report.AsOf.Format(time.RFC3339Nano) || r.Values[1] != report.Timezone {
			t.Fatal("snapshot metadata drift")
		}
		var ref financialExportReference
		if err := json.Unmarshal(r.Reference, &ref); err != nil {
			t.Fatal(err)
		}
		if err := validateFinancialReference(ref, report.Kind); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func TestFinancialCSVEmptyTotalsAndFailure(t *testing.T) {
	for _, tc := range []struct {
		kind        authorization.FinancialReportKind
		width, rows int
	}{
		{authorization.FinancialAging, 13, 7}, {authorization.FinancialCustomers, 15, 1}, {authorization.FinancialSettlement, 26, 16}, {authorization.FinancialCredit, 13, 1}, {authorization.FinancialCombined, 30, 17},
	} {
		report := financialTestReport(tc.kind)
		if got := len(financialTestRecords(t, report, tc.width)); got != tc.rows {
			t.Fatalf("%s: %d rows != %d", tc.kind, got, tc.rows)
		}
		failure := errors.New("storage failed")
		calls := 0
		err := emitFinancialReport(report, func(Record) error { calls++; return failure })
		if !errors.Is(err, failure) || calls != 1 {
			t.Fatalf("%s swallowed emission failure: calls=%d err=%v", tc.kind, calls, err)
		}
	}
}

const financialPaymentID = "10000000-0000-4000-8000-000000000001"
const financialCreditID = "20000000-0000-4000-8000-000000000001"
const financialRefundID = "30000000-0000-4000-8000-000000000001"
const financialReversalID = "40000000-0000-4000-8000-000000000001"

func financialTestEvent(kind, id string, amount, cash int64, at *time.Time) services.FinancialActivityRow {
	event := services.FinancialActivityRow{Kind: kind, ID: id, CustomerID: 7, Customer: "=Customer, Inc.\nSecond line", RecordedAt: at, EffectiveDate: "2025-01-01", AmountCents: amount, NetPaymentsCents: cash,
		Reference: authorization.FinancialReference{Facet: authorization.FinancialSettlement, CustomerID: 7, EventKind: kind, EventID: id}}
	if kind == "payment" {
		event.PaymentID = id
	}
	return event
}

func TestFinancialCSVActivityUnknownAllocationsAndCurrentCredit(t *testing.T) {
	r := financialTestReport(authorization.FinancialCombined)
	r.Filter = services.FinancialReportFilter{From: "2026-11-23", To: "2026-11-23", Sort: "entry_asc"}
	paid := financialTestEvent("payment", financialPaymentID, 10000, 10000, &r.AsOf)
	paid.PaymentID = financialPaymentID
	refund := financialTestEvent("credit_refund", financialRefundID, 2000, -2000, &r.AsOf)
	refund.Allocations = []services.FinancialSourceAllocation{
		{Kind: "refund_credit", ID: financialRefundID, CreditID: financialCreditID, PaymentID: financialPaymentID, SourceDate: "2025-01-01", AmountCents: 1500},
		{Kind: "refund_credit", ID: financialRefundID, CreditID: "20000000-0000-4000-8000-000000000002", PaymentID: "10000000-0000-4000-8000-000000000002", SourceDate: "2025-01-02", AmountCents: 500},
	}
	reversal := financialTestEvent("refund_reversal", financialReversalID, 2000, 2000, &r.AsOf)
	reversal.OriginalKind, reversal.OriginalID = "credit_refund", financialRefundID
	reversal.Allocations = append([]services.FinancialSourceAllocation{}, refund.Allocations...)
	for i := range reversal.Allocations {
		reversal.Allocations[i].AmountCents *= -1
	}
	r.Activity = []services.FinancialActivityRow{paid, refund, reversal}
	r.UnknownActivity = []services.FinancialActivityRow{financialTestEvent("payment", "10000000-0000-4000-8000-000000000003", 700, 700, nil)}
	r.Credits = []services.FinancialCreditRow{{ID: financialCreditID, PaymentID: financialPaymentID, CustomerID: 7, Customer: paid.Customer, SourceDate: "2025-01-01", RecordedAt: &r.AsOf, OriginalCents: 4000, AvailableCents: 4000, Reference: authorization.FinancialReference{Facet: authorization.FinancialCredit, CustomerID: 7, CreditID: financialCreditID}}}
	r.Totals.AvailableCreditCents = 4000
	rows := financialTestRecords(t, r, 30)
	counts := map[string]int{}
	for _, row := range rows {
		v := row.Values
		counts[v[2]]++
		if v[4] != r.Filter.From || v[5] != r.Filter.To {
			t.Fatal("period context lost")
		}
		switch v[2] {
		case "allocation":
			if v[22] != "" || v[23] != "" || v[24] != "" {
				t.Fatal("allocation duplicates additive event money/count")
			}
			if v[20] != financialRefundID || v[17] == "" || v[18] == "" {
				t.Fatal("persisted refund/source identities lost")
			}
			if v[9] == "refund_reversal" && !strings.HasPrefix(v[25], "-") {
				t.Fatal("compensating allocation lost sign")
			}
		case "event":
			if v[3] == "unknown_entry_time" && (v[14] != "" || v[15] != "false") {
				t.Fatal("fabricated unknown entry date")
			}
		case "net_payments_total":
			want := "100.00"
			if v[3] == "unknown_entry_time" {
				want = "7.00"
			}
			if v[24] != want {
				t.Fatalf("section %s net=%s want=%s", v[3], v[24], want)
			}
		case "credit_source":
			if v[3] != "current_credit" || v[24] != "" || v[29] != "40.00" || v[26] == "" || v[27] != "true" {
				t.Fatal("current Credit mixed with activity or source entry evidence lost")
			}
		}
	}
	if counts["event"] != 4 || counts["allocation"] != 4 || counts["credit_source"] != 1 {
		t.Fatalf("unexpected detail counts: %v", counts)
	}
	// Changing only the period cannot filter the supplied current snapshot.
	r.Filter.From, r.Filter.To = "2020-01-01", "2020-01-02"
	other := financialTestRecords(t, r, 30)
	if other[len(other)-1].Values[29] != "40.00" {
		t.Fatal("period changed available Credit")
	}
}

func TestFinancialCSVAggregateContributorReferences(t *testing.T) {
	for _, kind := range []authorization.FinancialReportKind{authorization.FinancialAging, authorization.FinancialCustomers} {
		r := financialTestReport(kind)
		ref := authorization.FinancialReference{Facet: kind, CustomerID: 7, Invoices: []authorization.FinancialInvoiceReference{{ID: 11, JobID: 5}, {ID: 12, JobID: 6}}}
		r.Totals.InvoiceCount, r.Totals.TotalCents, r.Totals.SettledCents, r.Totals.ReceivableCents = 2, 30000, 10000, 20000
		r.Totals.Buckets[0] = 20000
		width := 15
		if kind == authorization.FinancialCustomers {
			r.Customers = []services.FinancialCustomerRow{{ID: 7, Name: "Customer", InvoiceCount: 2, TotalCents: 30000, SettledCents: 10000, ReceivableCents: 20000, Buckets: r.Totals.Buckets, Reference: ref}}
		} else {
			width = 13
			r.Invoices = []services.FinancialInvoiceRow{{ID: 11, CustomerID: 7, TotalCents: 30000, SettledCents: 10000, DueCents: 20000, Bucket: "not_yet_due", Reference: ref}}
		}
		rows := financialTestRecords(t, r, width)
		var stored financialExportReference
		if err := json.Unmarshal(rows[len(rows)-1].Reference, &stored); err != nil {
			t.Fatal(err)
		}
		if !stored.Summary || len(stored.References) == 0 || !reflect.DeepEqual(stored.References[0].Invoices, ref.Invoices) {
			t.Fatal("aggregate lost original Invoice/Job contributors")
		}
	}
	credit := authorization.FinancialReference{Facet: authorization.FinancialCredit, CustomerID: 7, CreditID: financialCreditID}
	if _, err := financialReferenceBytes(authorization.FinancialSettlement, false, []authorization.FinancialReference{credit}); err == nil {
		t.Fatal("settlement must not substitute current Credit authority")
	}
	if _, err := financialReferenceBytes(authorization.FinancialCredit, false, nil); err == nil {
		t.Fatal("empty detail reference accepted")
	}
	if err := validateFinancialReference(financialExportReference{}, authorization.FinancialCredit); err == nil {
		t.Fatal("unversioned/malformed reference accepted")
	}
}
