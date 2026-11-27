package exports

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
)

func TestLeaseExportFixedContracts(t *testing.T) {
	doc, err := os.ReadFile("../../docs/v1-ticket-101-interfaces.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		mode  authorization.LeaseExportMode
		kind  string
		width int
	}{
		{authorization.LeaseExportOperational, LeaseOperationalKind, 27},
		{authorization.LeaseExportBilling, LeaseBillingKind, 59},
	} {
		d := Leases{Mode: test.mode}
		if d.Kind() != test.kind || d.Permission() != "lease.export" || d.Scope() != authorization.All || len(d.Columns()) != test.width {
			t.Fatalf("invalid contract: %+v", test)
		}
		if !strings.Contains(string(doc), "```text\n"+strings.Join(d.Columns(), ",")+"\n```") {
			t.Fatalf("%s differs from frozen contract", d.Kind())
		}
		columns := d.Columns()
		columns[0] = "mutated"
		if d.Columns()[0] != "row_type" {
			t.Fatal("mutable column contract")
		}
		if err := d.Authorize(authorization.Principal{}, json.RawMessage(`{}`)); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("unauthorized empty selection: %v", err)
		}
	}
	if (Leases{}).Kind() != "" || (Leases{}).Columns() != nil || (Leases{}).Validate(json.RawMessage(`{}`)) == nil {
		t.Fatal("invalid mode fell back to operational")
	}
}

func TestLeaseExportStrictFilters(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `[]`, `true`, `"object"`, `{`, `{} {}`, `{"page":1}`, `{"per_page":100}`,
		`{"mode":"billing"}`, `{"scope":"A"}`, `{"queue":"starts"}`, `{"Search":"private"}`,
		`{"search":null}`, `{"customer":null}`, `{"customer":"1"}`, `{"customer":1.5}`,
		`{"customer":9223372036854775808}`, `{"customer":-1}`, `{"asset":-1}`, `{"premises":-1}`,
		`{"search":"one","search":"two"}`, `{"search":"one","search":"one"}`,
		`{"as_of":"2026-02-30"}`, `{"ending_from":"2026-06-30","ending_through":"2026-06-01"}`,
		`{"lifecycle":"unknown"}`, `{"billing":"paid"}`, `{"sort":"title"}`, `{"archive":"true"}`, `{"placement":"any"}`,
	} {
		if _, err := DecodeLeaseExportFilter(json.RawMessage(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	f, err := DecodeLeaseExportFilter(json.RawMessage(`{"search":" MiXeD café ","customer":42,"ending_from":"2026-06-01","ending_through":"2026-06-30"}`))
	if err != nil || f.Search != " MiXeD café " || f.Customer != 42 || f.Sort != "number" || f.AsOf != "" {
		t.Fatalf("normalization: %+v %v", f, err)
	}
	for _, billing := range []string{"review", "void", "ungenerated", "pending", "failed"} {
		raw := json.RawMessage(`{"billing":"` + billing + `"}`)
		if err := (Leases{Mode: authorization.LeaseExportBilling}).Validate(raw); err != nil {
			t.Fatalf("billing %s: %v", billing, err)
		}
		if err := (Leases{Mode: authorization.LeaseExportOperational}).Validate(raw); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("operational financial predicate %s: %v", billing, err)
		}
	}
}

func TestLeaseExportStrictReferences(t *testing.T) {
	for _, raw := range []string{
		``, `null`, `[]`, `{`, `{} {}`, `{"unexpected":1}`,
		`{"filter":{"page":1}}`, `{"leases":[{"id":1,"invoice_id":2}]}`,
		`{"leases":[{"id":1,"assets":[{"id":2,"title":"hidden"}]}]}`,
		`{"leases":[{"id":1,"invoices":[{"id":2,"pricing":true}]}]}`,
	} {
		if _, err := decodeLeaseExportReference(json.RawMessage(raw)); !errors.Is(err, authorization.ErrForbidden) {
			t.Errorf("reference %s: %v", raw, err)
		}
	}
	ref := authorization.LeaseExportReference{Version: 1, Mode: authorization.LeaseExportBilling, Summary: true, Filter: authorization.LeaseExportFilter{Sort: "number", Customer: 3}, Selection: leaseExportTestSelection(3), CompleteGeneration: true, Leases: []authorization.LeaseExportLeaseReference{}}
	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeLeaseExportReference(raw)
	if err != nil || !reflect.DeepEqual(ref, decoded) {
		t.Fatalf("empty summary prerequisites lost: %+v %v", decoded, err)
	}
	// Even an empty summary cannot bypass domain authority (and must not need a
	// transaction before rejecting a principal with no grants).
	if err := (Leases{Mode: authorization.LeaseExportBilling}).Recheck(context.Background(), nil, authorization.Principal{}, raw); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("empty summary authorized: %v", err)
	}
}

func TestLeaseExportMalformedRequiredReferencesAdapter(t *testing.T) {
	ref := leaseExportTestReference(authorization.LeaseExportBilling)
	ref.Selection = leaseExportTestSelection(3)
	ref.Selection.Premises = []authorization.LeaseExportRelatedReference{{ID: 4, Archived: true}}
	ref.Selection.Assets = []authorization.LeaseExportRelatedReference{{ID: 5, Archived: true}}
	ref.Filter.Customer, ref.Filter.Premises, ref.Filter.Asset = 3, 4, 5
	ref.Leases[0].Customers = []authorization.LeaseExportRelatedReference{{ID: 3}}
	ref.Leases[0].Premises = []authorization.LeaseExportRelatedReference{{ID: 4}}
	ref.Leases[0].Assets = []authorization.LeaseExportRelatedReference{{ID: 5}}
	ref.Leases[0].Invoices = []authorization.LeaseExportInvoiceReference{{ID: 6}}
	ref.Leases[0].Actors = []int64{7}
	raw, err := json.Marshal(ref)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeLeaseExportReference(raw); err != nil {
		t.Fatal(err)
	}
	// Mutate each mandatory key at each object depth independently. Retain all
	// other valid anchors so this proves omissions cannot silently zero fields.
	for _, path := range [][]string{
		{}, {"filter"}, {"selection"}, {"selection", "customers", "0"}, {"selection", "premises", "0"}, {"selection", "assets", "0"}, {"leases", "0"}, {"leases", "0", "customers", "0"},
		{"leases", "0", "premises", "0"}, {"leases", "0", "assets", "0"}, {"leases", "0", "invoices", "0"},
	} {
		var root map[string]any
		if err := json.Unmarshal(raw, &root); err != nil {
			t.Fatal(err)
		}
		var at any = root
		for _, segment := range path {
			if segment == "0" {
				at = at.([]any)[0]
			} else {
				at = at.(map[string]any)[segment]
			}
		}
		object := at.(map[string]any)
		for key, original := range object {
			delete(object, key)
			missing, _ := json.Marshal(root)
			if _, err := decodeLeaseExportReference(missing); !errors.Is(err, authorization.ErrForbidden) {
				t.Errorf("missing %v.%s accepted", path, key)
			}
			object[key] = nil
			null, _ := json.Marshal(root)
			if _, err := decodeLeaseExportReference(null); !errors.Is(err, authorization.ErrForbidden) {
				t.Errorf("null %v.%s accepted", path, key)
			}
			object[key] = original
		}
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"archived":false`, `"archived":false,"archived":true`, 1),
		strings.Replace(string(raw), `"summary":false`, `"Summary":false`, 1),
		strings.Replace(string(raw), `"actors":[7]`, `"actors":[null]`, 1),
	} {
		if _, err := decodeLeaseExportReference(json.RawMessage(bad)); !errors.Is(err, authorization.ErrForbidden) {
			t.Errorf("ambiguous private anchor accepted: %s", bad)
		}
	}
}

func leaseExportTestMeta() authorization.LeaseExportMeta {
	return authorization.LeaseExportMeta{AsOf: "2026-06-10", Timezone: "America/New_York", SnapshotAt: time.Date(2026, 6, 10, 12, 0, 0, 123, time.FixedZone("offset", -4*60*60))}
}

func leaseExportTestReference(mode authorization.LeaseExportMode) authorization.LeaseExportReference {
	return authorization.LeaseExportReference{Version: 1, Mode: mode, Filter: authorization.LeaseExportFilter{Sort: "number"}, Selection: leaseExportTestSelection(0), Leases: []authorization.LeaseExportLeaseReference{{ID: 12,
		Customers: []authorization.LeaseExportRelatedReference{}, Premises: []authorization.LeaseExportRelatedReference{}, Assets: []authorization.LeaseExportRelatedReference{}, Invoices: []authorization.LeaseExportInvoiceReference{}, Actors: []int64{},
	}}}
}

func leaseExportTestSelection(customer int64) authorization.LeaseExportSelectionReference {
	selection := authorization.LeaseExportSelectionReference{Customers: []authorization.LeaseExportRelatedReference{}, Premises: []authorization.LeaseExportRelatedReference{}, Assets: []authorization.LeaseExportRelatedReference{}}
	if customer != 0 {
		selection.Customers = append(selection.Customers, authorization.LeaseExportRelatedReference{ID: customer, Archived: true})
	}
	return selection
}

func TestLeaseExportEmptySelectionArchivedAnchors(t *testing.T) {
	ref := leaseExportTestReference(authorization.LeaseExportBilling)
	ref.Summary = true
	ref.Leases = []authorization.LeaseExportLeaseReference{}
	ref.Filter.Customer, ref.Filter.Premises, ref.Filter.Asset = 3, 4, 5
	ref.Selection = leaseExportTestSelection(3)
	ref.Selection.Premises = []authorization.LeaseExportRelatedReference{{ID: 4, Archived: true}}
	ref.Selection.Assets = []authorization.LeaseExportRelatedReference{{ID: 5, Archived: true}}
	var encoded json.RawMessage
	if err := (Leases{Mode: ref.Mode}).emitSummary(authorization.LeaseExportSummary{Meta: leaseExportTestMeta(), RentAvailable: true, Reference: ref}, func(row Record) error {
		encoded = row.Reference
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeLeaseExportReference(encoded)
	if err != nil || !reflect.DeepEqual(ref, decoded) {
		t.Fatalf("empty-result original archive anchors lost: %+v %v", decoded, err)
	}
	for _, key := range []string{"customers", "premises", "assets"} {
		for _, malformed := range []string{`null`, `{}`, `false`, `[null]`, `[{}]`, `[{"id":3}]`, `[{"id":3,"archived":null}]`, `[{"id":3,"archived":false,"archived":true}]`, `[{"id":3,"Archived":true}]`, `[{"id":3,"archived":"true"}]`, `[{"id":3,"archived":true,"unknown":false}]`} {
			var root map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &root); err != nil {
				t.Fatal(err)
			}
			var selection map[string]json.RawMessage
			if err := json.Unmarshal(root["selection"], &selection); err != nil {
				t.Fatal(err)
			}
			selection[key] = json.RawMessage(malformed)
			root["selection"], err = json.Marshal(selection)
			if err != nil {
				t.Fatal(err)
			}
			bad, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeLeaseExportReference(bad); !errors.Is(err, authorization.ErrForbidden) {
				t.Errorf("accepted selection.%s=%s", key, malformed)
			}
		}
	}
}

func TestLeaseExportOperationalIsolationAndHistory(t *testing.T) {
	d := Leases{Mode: authorization.LeaseExportOperational}
	entry := authorization.LeaseExportEntry{Lease: authorization.LeaseReportRow{
		ID: 12, Number: "L-12", Title: "=Lease, \"quoted\"\n日本語", StartsOn: "2026-01-01", EndsOn: "2026-06-30", EndingOn: "2026-06-20",
		LeaseCalendar: authorization.LeaseCalendar{Lifecycle: "active"},
		Assets:        []authorization.LeaseReportAsset{{ID: 7, Number: "A-7", Name: "Pump", Placement: "unknown", StartsOn: "2026-01-01"}},
	}}
	entry.FirstMonthlyOn = "2026-07-01"
	entry.PositionAvailability = "available"
	entry.Reference = leaseExportTestReference(d.Mode)
	var before, after []Record
	if err := d.emitEntry(leaseExportTestMeta(), entry, func(r Record) error { before = append(before, r); return nil }); err != nil {
		t.Fatal(err)
	}
	entry.Lease.RentCents = 987654321
	entry.Lease.ScheduledRentCents = 999999
	entry.Lease.Invoices = []authorization.LeaseReportInvoice{{Number: "SECRET", RecordedCents: 777}}
	entry.Lease.Adjustments = []authorization.LeaseAdjustmentRecord{{Reason: "PRIVATE"}}
	entry.RentTerms = []authorization.LeaseExportRentTerm{{RentCents: 12345, TaxRate: "SECRET"}}
	entry.FailureHistory = "PRIVATE"
	entry.GeneratedInvoices = []authorization.LeaseExportGeneratedInvoice{{Invoice: authorization.LeaseReportInvoice{Number: "SECRET"}}}
	if err := d.emitEntry(leaseExportTestMeta(), entry, func(r Record) error { after = append(after, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("operational bytes depend on financial facts")
	}
	if len(before) != 2 || before[0].Values[26] != "available" || before[1].Values[26] != "unavailable" || before[1].Values[24] != "unknown" {
		t.Fatalf("history states: %+v", before)
	}
	for _, row := range before {
		if len(row.Values) != 27 || row.Values[9] != "" || row.Values[11] != "" {
			t.Fatal("bad width or redacted IDs")
		}
	}
	if before[0].Values[16] != "2026-07-01" {
		t.Fatal("monthly transition lost")
	}
	entry.Lease.PositionUnavailable = true
	entry.PositionAvailability = "unavailable"
	if err := d.emitEntry(leaseExportTestMeta(), entry, func(r Record) error {
		if r.Values[26] != "unavailable" {
			t.Fatal("position history claimed available")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseExportBillingRowsExactCentsAndProvenance(t *testing.T) {
	d := Leases{Mode: authorization.LeaseExportBilling}
	entry := authorization.LeaseExportEntry{
		Lease: authorization.LeaseReportRow{ID: 12, Number: "L-12", Title: " =Lease, \"quoted\"\n日本語", LeaseCalendar: authorization.LeaseCalendar{Lifecycle: "active"}, RentCents: 10001,
			Invoices: []authorization.LeaseReportInvoice{{ID: 999}}, Periods: []authorization.LeaseReportPeriod{{State: "duplicate"}},
			Adjustments: []authorization.LeaseAdjustmentRecord{{ID: 8, InvoiceID: 5, AmountCents: math.MinInt64, Kind: "abatement", Reason: "@reason", RecordedAt: "2026-06-10 12:00:00+00"}},
		},
		FailureHistory: "unavailable", PositionAvailability: "available", Reference: leaseExportTestReference(d.Mode),
		RentTerms:        []authorization.LeaseExportRentTerm{{RentCents: 10001, TaxRate: "8.125", EffectiveOn: "2026-06-10"}},
		BillingRevisions: []authorization.LeaseExportBillingRevision{{ID: 4, EffectiveOn: "2026-07-01", RecordedAt: "2026-06-10T08:00:00-04:00", Reason: "Approved", Changes: []authorization.LeaseExportBillingChange{{Ordinal: 2, BeforeRentCents: 10001, AfterRentCents: 12345, BeforeTaxRate: "8.125", AfterTaxRate: "9"}}}},
		Periods:          []authorization.LeaseExportPeriod{{Kind: "monthly", DueOn: "2026-07-01", State: "pending"}},
		GeneratedInvoices: []authorization.LeaseExportGeneratedInvoice{{ObligationID: 3, ObligationRevisionID: 4, TaxCents: 812, TaxRate: "8.125",
			Invoice: authorization.LeaseReportInvoice{ID: 5, Number: "I-5", Kind: "monthly", DueOn: "2026-06-01", GeneratedAt: "2026-05-01 08:00:00.123456-04", RecordedCents: 10813, ManualStatus: "invoice:draft", Status: "invoice:paid", Review: true,
				Settlements: []authorization.LeaseReportSettlement{{ID: "settlement-uuid", Kind: "payment", AmountCents: 101, EffectiveOn: "2026-06-01", ReversedOn: "2026-06-02"}},
			},
			Lines: []authorization.LeaseExportInvoiceLine{{Ordinal: 1, Amount: "100.01", Taxable: true, BillingRevisionID: 4}},
		}},
	}
	var rows []Record
	if err := d.emitEntry(leaseExportTestMeta(), entry, func(r Record) error { rows = append(rows, r); return nil }); err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"lease", "rent", "billing_revision", "period", "invoice", "invoice_asset", "settlement", "adjustment"}
	if len(rows) != len(wantKinds) {
		t.Fatalf("duplicate/missing typed rows: %d", len(rows))
	}
	columns := d.Columns()
	cell := func(row int, name string) string {
		t.Helper()
		for i, column := range columns {
			if name == column {
				return rows[row].Values[i]
			}
		}
		t.Fatalf("missing column %s", name)
		return ""
	}
	for i, row := range rows {
		if len(row.Values) != 59 || cell(i, "row_type") != wantKinds[i] || cell(i, "lease_id") != "12" || cell(i, "snapshot_at") != "2026-06-10T16:00:00.000000123Z" {
			t.Fatalf("row %d: %v", i, row.Values)
		}
	}
	for _, test := range []struct {
		row           int
		column, value string
	}{
		{0, "monthly_rent", "100.01"}, {0, "failure_history", "unavailable"}, {1, "asset_id", ""}, {1, "tax_rate", "8.125"},
		{2, "before_rent", "100.01"}, {2, "after_rent", "123.45"}, {2, "recorded_at", "2026-06-10T12:00:00Z"}, {2, "actor_id", ""},
		{3, "period_state", "pending"}, {3, "obligation_id", ""}, {4, "generated_total", "108.13"}, {4, "generated_tax", "8.12"},
		{4, "generated_at", "2026-05-01T12:00:00.123456Z"}, {4, "effective_invoice_status", "unavailable"}, {4, "manual_invoice_status", "invoice:draft"},
		{4, "review_required", "true"}, {5, "line_ordinal", "1"}, {5, "asset_id", ""}, {5, "line_amount", "100.01"}, {5, "generated_total", ""},
		{5, "obligation_revision_id", "4"}, {6, "settlement_amount", "1.01"}, {6, "generated_total", ""}, {7, "adjustment_amount", "-92233720368547758.08"},
		{7, "invoice_number", ""}, {7, "reason", "@reason"},
		{2, "revision_line_ordinal", "2"}, {2, "history_status", "available"}, {0, "history_status", "available"},
	} {
		if got := cell(test.row, test.column); got != test.value {
			t.Errorf("%s.%s = %q, want %q", wantKinds[test.row], test.column, got, test.value)
		}
	}
	// Exercise the engine's reversible spreadsheet escaping plus RFC CSV quoting.
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	for _, row := range rows {
		values := append([]string{}, row.Values...)
		for i := range values {
			values[i] = safeCell(values[i])
		}
		if err := w.Write(values); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	decoded, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if decoded[0][6] != "'"+entry.Lease.Title || decoded[7][51] != "'-92233720368547758.08" || decoded[7][17] != "'@reason" {
		t.Fatal("CSV escaping changed exact text/money")
	}
}

func TestLeaseExportSummaryAndEmitFailure(t *testing.T) {
	stop := errors.New("sink failed")
	for _, mode := range []authorization.LeaseExportMode{authorization.LeaseExportOperational, authorization.LeaseExportBilling} {
		d := Leases{Mode: mode}
		calls := 0
		entry := authorization.LeaseExportEntry{Lease: authorization.LeaseReportRow{Assets: []authorization.LeaseReportAsset{{ID: 1}}}, RentTerms: []authorization.LeaseExportRentTerm{{RentCents: 1}}}
		entry.PositionAvailability, entry.Reference = "available", leaseExportTestReference(mode)
		if err := d.emitEntry(leaseExportTestMeta(), entry, func(Record) error { calls++; return stop }); !errors.Is(err, stop) || calls != 1 {
			t.Fatalf("emit failure swallowed: calls=%d err=%v", calls, err)
		}
		summary := authorization.LeaseExportSummary{Meta: leaseExportTestMeta(), LeaseCount: 0, RentCents: 0, RentAvailable: true, Reference: authorization.LeaseExportReference{Version: 1, Mode: mode, Summary: true, Leases: []authorization.LeaseExportLeaseReference{}, Filter: authorization.LeaseExportFilter{Customer: 9, Sort: "number"}, Selection: leaseExportTestSelection(9)}}
		if err := d.emitSummary(summary, func(record Record) error {
			ref, err := decodeLeaseExportReference(record.Reference)
			if err != nil || !reflect.DeepEqual(ref, summary.Reference) {
				t.Fatal("summary lost selection reference")
			}
			for i, column := range d.Columns() {
				want := ""
				switch column {
				case "row_type":
					want = "summary"
				case "as_of":
					want = summary.Meta.AsOf
				case "timezone":
					want = summary.Meta.Timezone
				case "snapshot_at":
					want = "2026-06-10T16:00:00.000000123Z"
				case "matching_lease_count":
					want = "0"
				case "matching_monthly_rent":
					want = "0.00"
				case "history_status":
					if mode == authorization.LeaseExportBilling {
						want = "available"
					}
				}
				if record.Values[i] != want {
					t.Errorf("summary %s: %q want %q", column, record.Values[i], want)
				}
			}
			return stop
		}); !errors.Is(err, stop) {
			t.Fatalf("summary emit failure swallowed: %v", err)
		}
	}
	if _, err := leaseExportInstant("corrupt retained timestamp"); err == nil {
		t.Fatal("malformed retained fact exported")
	}
}

func TestLeaseExportUnavailablePositionAndOriginalRevision(t *testing.T) {
	d := Leases{Mode: authorization.LeaseExportBilling}
	entry := authorization.LeaseExportEntry{PositionAvailability: "unavailable", Reference: leaseExportTestReference(d.Mode),
		Lease:             authorization.LeaseReportRow{ID: 12, LeaseCalendar: authorization.LeaseCalendar{Lifecycle: "unavailable"}},
		GeneratedInvoices: []authorization.LeaseExportGeneratedInvoice{{ObligationID: 1, Invoice: authorization.LeaseReportInvoice{ID: 2, GeneratedAt: "2026-06-01T00:00:00Z"}, Lines: []authorization.LeaseExportInvoiceLine{{Ordinal: 1, Amount: "0.00"}}}},
	}
	if err := d.emitEntry(leaseExportTestMeta(), entry, func(row Record) error {
		switch row.Values[0] {
		case "lease":
			if row.Values[18] != "unavailable" || row.Values[19] != "unavailable" || row.Values[58] != "unavailable" {
				t.Fatal("missing position reported as zero rent")
			}
		case "invoice", "invoice_asset":
			if row.Values[30] != "0" || row.Values[58] != "available" {
				t.Fatal("retained original revision/availability lost")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ref := leaseExportTestReference(d.Mode)
	ref.Summary = true
	if err := d.emitSummary(authorization.LeaseExportSummary{Meta: leaseExportTestMeta(), LeaseCount: 1, RentCents: 12345, RentAvailable: false, Reference: ref}, func(row Record) error {
		if row.Values[55] != "unavailable" || row.Values[58] != "unavailable" {
			t.Fatal("incomplete total reported as numeric amount")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
