package exports

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/jackc/pgx/v5"
)

const (
	LeaseOperationalKind = "lease-operational-A-v1"
	LeaseBillingKind     = "lease-billing-A-v1"
)

// Leases encodes the authorized transaction-bound projection. Mode permanently
// fixes the contract; operational rows never inspect financial fields.
type Leases struct{ Mode authorization.LeaseExportMode }

var _ Definition = Leases{}
var _ selectionAuthorizer = Leases{}

func (d Leases) Kind() string {
	switch d.Mode {
	case authorization.LeaseExportOperational:
		return LeaseOperationalKind
	case authorization.LeaseExportBilling:
		return LeaseBillingKind
	default:
		return ""
	}
}
func (Leases) Permission() string         { return "lease.export" }
func (Leases) Scope() authorization.Scope { return authorization.All }
func (d Leases) Columns() []string {
	const common = "row_type,as_of,timezone,snapshot_at,lease_id,lease_number,title,lifecycle,archived,customer_id,customer,premises_id,premises,"
	switch d.Mode {
	case authorization.LeaseExportOperational:
		return strings.Split(common+"starts_on,fixed_ends_on,termination_on,first_monthly_on,renewal_starts_on,renewal_ends_on,asset_id,asset_number,asset_name,coverage_starts_on,coverage_ends_on,placement,matching_lease_count,history_status", ",")
	case authorization.LeaseExportBilling:
		return strings.Split(common+"asset_id,effective_on,recorded_at,actor_id,reason,monthly_rent,scheduled_monthly_rent,tax_rate,revision_id,before_rent,after_rent,before_tax_rate,after_tax_rate,period_kind,due_on,period_state,obligation_id,obligation_revision_id,invoice_id,invoice_number,generated_at,generated_total,generated_tax,tax_exempt,manual_invoice_status,effective_invoice_status,review_required,line_ordinal,line_amount,line_taxable,line_billing_revision_id,settlement_id,settlement_kind,settlement_amount,settlement_effective_on,reversed_on,adjustment_id,adjustment_kind,adjustment_amount,adjustment_starts_on,adjustment_ends_on,matching_lease_count,matching_monthly_rent,failure_history,revision_line_ordinal,history_status", ",")
	default:
		return nil
	}
}

// DecodeLeaseExportFilter accepts only the fixed selection object, never page,
// scope, mode, or arbitrary report fields. Duplicate/null values are ambiguous
// intents and are rejected rather than silently taking the last/default value.
func DecodeLeaseExportFilter(raw json.RawMessage) (authorization.LeaseExportFilter, error) {
	var f authorization.LeaseExportFilter
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return f, ErrConflict
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return f, ErrConflict
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return f, ErrConflict
		}
		seen[key] = true
		switch key {
		case "as_of", "search", "lifecycle", "customer", "asset", "premises", "ending_from", "ending_through", "archive", "placement", "billing", "sort":
		default:
			return f, ErrConflict
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return f, ErrConflict
		}
	}
	if _, err := d.Token(); err != nil {
		return f, ErrConflict
	}
	if d.Decode(new(any)) != io.EOF {
		return f, ErrConflict
	}
	if json.Unmarshal(raw, &f) != nil {
		return f, ErrConflict
	}
	f, err = authorization.NormalizeLeaseExportFilter(f)
	if err != nil {
		return f, ErrConflict
	}
	return f, nil
}

func (d Leases) Validate(raw json.RawMessage) error {
	if d.Kind() == "" {
		return ErrConflict
	}
	f, err := DecodeLeaseExportFilter(raw)
	if err != nil {
		return err
	}
	if d.Mode == authorization.LeaseExportOperational && f.Billing != "" {
		return authorization.ErrForbidden
	}
	return nil
}
func (d Leases) Authorize(p authorization.Principal, raw json.RawMessage) error {
	if err := d.Validate(raw); err != nil {
		return err
	}
	f, _ := DecodeLeaseExportFilter(raw)
	return authorization.AuthorizeLeaseExport(p, d.Mode, f)
}
func (d Leases) AuthorizeSelection(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	if err := d.Validate(raw); err != nil {
		return err
	}
	f, _ := DecodeLeaseExportFilter(raw)
	return authorization.AuthorizeLeaseExportSelection(ctx, tx, p, d.Mode, f)
}
func (d Leases) ValidateAcceptance(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage) error {
	if err := d.Validate(raw); err != nil {
		return err
	}
	f, _ := DecodeLeaseExportFilter(raw)
	return authorization.ValidateLeaseExportFreshSelection(ctx, tx, p, d.Mode, f)
}
func (d Leases) Snapshot(ctx context.Context, tx pgx.Tx, p authorization.Principal, raw json.RawMessage, emit func(Record) error) error {
	if err := d.Authorize(p, raw); err != nil {
		return err
	}
	f, _ := DecodeLeaseExportFilter(raw)
	summary, err := authorization.LeaseExportSnapshot(ctx, tx, p, d.Mode, f, func(meta authorization.LeaseExportMeta, entry authorization.LeaseExportEntry) error {
		return d.emitEntry(meta, entry, emit)
	})
	if err != nil {
		return err
	}
	return d.emitSummary(summary, emit)
}

func leaseExportID(id int64) string {
	if id == 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
func leaseExportPrefix(meta authorization.LeaseExportMeta, kind string, row *authorization.LeaseReportRow) []string {
	values := make([]string, 13)
	values[0], values[1], values[2], values[3] = kind, meta.AsOf, meta.Timezone, meta.SnapshotAt.UTC().Format(time.RFC3339Nano)
	if row != nil {
		copy(values[4:], []string{leaseExportID(row.ID), row.Number, row.Title, row.Lifecycle, strconv.FormatBool(row.Archived), leaseExportID(row.CustomerID), row.Customer, leaseExportID(row.PremisesID), row.Premises})
	}
	return values
}

func (d Leases) emitEntry(meta authorization.LeaseExportMeta, entry authorization.LeaseExportEntry, emit func(Record) error) error {
	ref, err := leaseExportReferenceBytes(entry.Reference, d.Mode)
	if err != nil {
		return err
	}
	if entry.PositionAvailability != "available" && entry.PositionAvailability != "unavailable" {
		return ErrConflict
	}
	if d.Mode == authorization.LeaseExportOperational {
		row := entry.Lease
		history := entry.PositionAvailability
		base := append(leaseExportPrefix(meta, "lease", &row), row.StartsOn, row.EndsOn, row.EndingOn, entry.FirstMonthlyOn, row.RenewalStartsOn, row.RenewalEndsOn)
		if history == "unavailable" {
			// A known retained date remains known; absent position-dependent dates
			// must not masquerade as an ordinary inapplicable blank.
			for i := 13; i < len(base); i++ {
				if base[i] == "" {
					base[i] = "unavailable"
				}
			}
		}
		values := append(append([]string{}, base...), "", "", "", "", "", "", "", history)
		if err := emit(Record{Values: values, Reference: ref}); err != nil {
			return err
		}
		for _, asset := range row.Assets {
			status := history
			if asset.Placement == "unknown" {
				status = "unavailable"
			}
			values := append(append([]string{}, base...), leaseExportID(asset.ID), asset.Number, asset.Name, asset.StartsOn, asset.EndsOn, asset.Placement, "", status)
			values[0] = "coverage"
			if err := emit(Record{Values: values, Reference: ref}); err != nil {
				return err
			}
		}
		return nil
	}
	if d.Mode != authorization.LeaseExportBilling {
		return authorization.ErrForbidden
	}
	return emitLeaseBilling(meta, entry, ref, emit)
}

func (d Leases) emitSummary(summary authorization.LeaseExportSummary, emit func(Record) error) error {
	ref, err := leaseExportReferenceBytes(summary.Reference, d.Mode)
	if err != nil {
		return err
	}
	values := leaseExportPrefix(summary.Meta, "summary", nil)
	switch d.Mode {
	case authorization.LeaseExportOperational:
		values = append(values, make([]string, 14)...)
		values[25] = strconv.FormatInt(summary.LeaseCount, 10)
	case authorization.LeaseExportBilling:
		row := leaseBillingCSV{MatchingLeases: strconv.FormatInt(summary.LeaseCount, 10), MatchingMonthlyRent: commercial.FormatCents(summary.RentCents), HistoryStatus: "available"}
		if !summary.RentAvailable {
			row.MatchingMonthlyRent, row.HistoryStatus = "unavailable", "unavailable"
		}
		values = append(values, row.values()...)
	default:
		return authorization.ErrForbidden
	}
	return emit(Record{Values: values, Reference: ref})
}

// Fixed typed row fields prevent unrestricted financial/revision JSON from
// entering the artifact. Every row starts blank except its applicable facts.
type leaseBillingCSV struct {
	AssetID, EffectiveOn, RecordedAt, ActorID, Reason                                    string
	MonthlyRent, ScheduledMonthlyRent, TaxRate, RevisionID                               string
	BeforeRent, AfterRent, BeforeTaxRate, AfterTaxRate                                   string
	PeriodKind, DueOn, PeriodState, ObligationID, ObligationRevisionID                   string
	InvoiceID, InvoiceNumber, GeneratedAt, GeneratedTotal, GeneratedTax, TaxExempt       string
	ManualInvoiceStatus, EffectiveInvoiceStatus, ReviewRequired                          string
	LineOrdinal, LineAmount, LineTaxable, LineBillingRevisionID                          string
	SettlementID, SettlementKind, SettlementAmount, SettlementEffectiveOn, ReversedOn    string
	AdjustmentID, AdjustmentKind, AdjustmentAmount, AdjustmentStartsOn, AdjustmentEndsOn string
	MatchingLeases, MatchingMonthlyRent, FailureHistory                                  string
	RevisionLineOrdinal, HistoryStatus                                                   string
}

func (r leaseBillingCSV) values() []string {
	return []string{r.AssetID, r.EffectiveOn, r.RecordedAt, r.ActorID, r.Reason, r.MonthlyRent, r.ScheduledMonthlyRent, r.TaxRate, r.RevisionID, r.BeforeRent, r.AfterRent, r.BeforeTaxRate, r.AfterTaxRate, r.PeriodKind, r.DueOn, r.PeriodState, r.ObligationID, r.ObligationRevisionID, r.InvoiceID, r.InvoiceNumber, r.GeneratedAt, r.GeneratedTotal, r.GeneratedTax, r.TaxExempt, r.ManualInvoiceStatus, r.EffectiveInvoiceStatus, r.ReviewRequired, r.LineOrdinal, r.LineAmount, r.LineTaxable, r.LineBillingRevisionID, r.SettlementID, r.SettlementKind, r.SettlementAmount, r.SettlementEffectiveOn, r.ReversedOn, r.AdjustmentID, r.AdjustmentKind, r.AdjustmentAmount, r.AdjustmentStartsOn, r.AdjustmentEndsOn, r.MatchingLeases, r.MatchingMonthlyRent, r.FailureHistory, r.RevisionLineOrdinal, r.HistoryStatus}
}

func emitLeaseBilling(meta authorization.LeaseExportMeta, entry authorization.LeaseExportEntry, ref json.RawMessage, emit func(Record) error) error {
	write := func(kind string, row leaseBillingCSV) error {
		var err error
		row.HistoryStatus = "available"
		if kind == "lease" || kind == "rent" {
			row.HistoryStatus = entry.PositionAvailability
		}
		if kind == "period" && row.PeriodState == "unavailable" {
			row.HistoryStatus = "unavailable"
		}
		if row.RecordedAt, err = leaseExportInstant(row.RecordedAt); err != nil {
			return err
		}
		if row.GeneratedAt, err = leaseExportInstant(row.GeneratedAt); err != nil {
			return err
		}
		return emit(Record{Values: append(leaseExportPrefix(meta, kind, &entry.Lease), row.values()...), Reference: ref})
	}
	rent, scheduled := commercial.FormatCents(entry.Lease.RentCents), commercial.FormatCents(entry.Lease.ScheduledRentCents)
	if entry.PositionAvailability == "unavailable" {
		rent, scheduled = "unavailable", "unavailable"
	}
	if err := write("lease", leaseBillingCSV{MonthlyRent: rent, ScheduledMonthlyRent: scheduled, FailureHistory: entry.FailureHistory}); err != nil {
		return err
	}
	for _, term := range entry.RentTerms {
		row := leaseBillingCSV{AssetID: leaseExportID(term.AssetID), EffectiveOn: term.EffectiveOn, TaxRate: term.TaxRate}
		if entry.Lease.Lifecycle == "scheduled" {
			row.ScheduledMonthlyRent = commercial.FormatCents(term.RentCents)
		} else {
			row.MonthlyRent = commercial.FormatCents(term.RentCents)
		}
		if err := write("rent", row); err != nil {
			return err
		}
	}
	for _, revision := range entry.BillingRevisions {
		for _, change := range revision.Changes {
			if err := write("billing_revision", leaseBillingCSV{AssetID: leaseExportID(change.AssetID), EffectiveOn: revision.EffectiveOn, RecordedAt: revision.RecordedAt, ActorID: leaseExportID(revision.ActorID), Reason: revision.Reason, RevisionID: leaseExportID(revision.ID), RevisionLineOrdinal: strconv.Itoa(change.Ordinal), BeforeRent: commercial.FormatCents(change.BeforeRentCents), AfterRent: commercial.FormatCents(change.AfterRentCents), BeforeTaxRate: change.BeforeTaxRate, AfterTaxRate: change.AfterTaxRate}); err != nil {
				return err
			}
		}
	}
	for _, period := range entry.Periods {
		if err := write("period", leaseBillingCSV{PeriodKind: period.Kind, DueOn: period.DueOn, PeriodState: period.State, ObligationID: leaseExportID(period.ObligationID), Reason: period.Reason}); err != nil {
			return err
		}
	}
	for _, generated := range entry.GeneratedInvoices {
		invoice := generated.Invoice
		status := "unavailable"
		if invoice.SettlementVisible {
			status = invoice.Status
		}
		if err := write("invoice", leaseBillingCSV{PeriodKind: invoice.Kind, DueOn: invoice.DueOn, ObligationID: leaseExportID(generated.ObligationID), ObligationRevisionID: strconv.FormatInt(generated.ObligationRevisionID, 10), InvoiceID: leaseExportID(invoice.ID), InvoiceNumber: invoice.Number, GeneratedAt: invoice.GeneratedAt, GeneratedTotal: commercial.FormatCents(invoice.RecordedCents), GeneratedTax: commercial.FormatCents(generated.TaxCents), TaxRate: generated.TaxRate, TaxExempt: strconv.FormatBool(generated.TaxExempt), ManualInvoiceStatus: invoice.ManualStatus, EffectiveInvoiceStatus: status, ReviewRequired: strconv.FormatBool(invoice.Review)}); err != nil {
			return err
		}
		for _, line := range generated.Lines {
			if err := write("invoice_asset", leaseBillingCSV{AssetID: leaseExportID(line.AssetID), InvoiceID: leaseExportID(invoice.ID), InvoiceNumber: invoice.Number, ObligationID: leaseExportID(generated.ObligationID), ObligationRevisionID: strconv.FormatInt(generated.ObligationRevisionID, 10), LineOrdinal: strconv.Itoa(line.Ordinal), LineAmount: line.Amount, LineTaxable: strconv.FormatBool(line.Taxable), LineBillingRevisionID: leaseExportID(line.BillingRevisionID)}); err != nil {
				return err
			}
		}
		for _, settlement := range invoice.Settlements {
			if err := write("settlement", leaseBillingCSV{InvoiceID: leaseExportID(invoice.ID), InvoiceNumber: invoice.Number, SettlementID: settlement.ID, SettlementKind: settlement.Kind, SettlementAmount: commercial.FormatCents(settlement.AmountCents), SettlementEffectiveOn: settlement.EffectiveOn, ReversedOn: settlement.ReversedOn}); err != nil {
				return err
			}
		}
	}
	for _, adjustment := range entry.Lease.Adjustments {
		if err := write("adjustment", leaseBillingCSV{AdjustmentID: leaseExportID(adjustment.ID), AdjustmentKind: adjustment.Kind, AdjustmentAmount: commercial.FormatCents(adjustment.AmountCents), AdjustmentStartsOn: adjustment.StartsOn, AdjustmentEndsOn: adjustment.EndsOn, InvoiceID: leaseExportID(adjustment.InvoiceID), AssetID: leaseExportID(adjustment.AssetID), ActorID: leaseExportID(adjustment.ActorID), RecordedAt: adjustment.RecordedAt, Reason: adjustment.Reason}); err != nil {
			return err
		}
	}
	return nil
}

// Existing report projections include PostgreSQL text timestamps. Normalize
// these retained instants, rejecting malformed facts instead of exporting them.
func leaseExportInstant(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999Z07"} {
		if instant, err := time.Parse(layout, value); err == nil {
			return instant.UTC().Format(time.RFC3339Nano), nil
		}
	}
	return "", ErrConflict
}
