package exports

import (
	"sort"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
)

var financialEventTypes = [...]string{"payment", "credit_creation", "credit_application", "credit_refund", "payment_reversal", "application_reversal", "refund_reversal"}

func financialEventType(kind string) (string, bool) {
	for _, value := range financialEventTypes {
		if kind == value {
			return value, true
		}
	}
	return "", false
}

func financialActivityValues(report services.FinancialReport, rowType, section string) []string {
	width := len(financialSettlementColumns())
	if report.Kind == authorization.FinancialCombined {
		width = len(financialCombinedColumns())
	}
	values := make([]string, width)
	copy(values, financialPrefix(report, rowType))
	values[3], values[4], values[5] = section, report.Filter.From, report.Filter.To
	return values
}

type financialEventTotal struct{ count, amount, cash int64 }

func emitFinancialActivity(report services.FinancialReport, emit func(Record) error) error {
	section := "dated_history"
	if report.Filter.From != "" || report.Filter.To != "" {
		section = "period"
	}
	if err := emitFinancialActivitySection(report, section, report.Activity, false, emit); err != nil {
		return err
	}
	return emitFinancialActivitySection(report, "unknown_entry_time", report.UnknownActivity, true, emit)
}

func emitFinancialActivitySection(report services.FinancialReport, section string, events []services.FinancialActivityRow, unknown bool, emit func(Record) error) error {
	totals := map[string]financialEventTotal{}
	var totalCount, totalCash int64
	for _, event := range events {
		if _, ok := financialEventType(event.Kind); !ok || (event.RecordedAt == nil) != unknown {
			return errFinancialExportFacts
		}
		entered, known := financialInstant(event.RecordedAt)
		values := financialActivityValues(report, "event", section)
		values[6], values[7], values[8] = strconv.FormatInt(event.CustomerID, 10), event.Customer, strconv.FormatBool(event.Archived)
		values[9], values[10], values[11], values[12] = event.Kind, event.ID, event.OriginalKind, event.OriginalID
		values[13], values[14], values[15] = event.EffectiveDate, entered, known
		values[16], values[17], values[18] = optionalExportID(event.InvoiceID), event.CreditID, event.PaymentID
		values[22], values[23], values[24] = "1", financialMoney(event.AmountCents), financialMoney(event.NetPaymentsCents)
		refs := financialActivityReferences(event)
		provenance := financialEventProvenance(event)
		if err := emitFinancialRecord(report, values, false, refs, emit, provenance); err != nil {
			return err
		}

		// These are already persisted allocations, not FIFO recomputation. Only
		// their presentation order changes; signed amounts and identities do not.
		allocations := append([]services.FinancialSourceAllocation{}, event.Allocations...)
		sort.Slice(allocations, func(i, j int) bool {
			a, b := allocations[i], allocations[j]
			if (a.CreditID == "") != (b.CreditID == "") {
				return a.CreditID == ""
			}
			if (a.SourceDate == "") != (b.SourceDate == "") {
				return a.SourceDate != ""
			}
			if a.SourceDate != b.SourceDate {
				return a.SourceDate < b.SourceDate
			}
			if a.CreditID != b.CreditID {
				return a.CreditID < b.CreditID
			}
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			return a.ID < b.ID
		})
		for _, allocation := range allocations {
			if !financialUUID(allocation.ID) {
				return errFinancialExportFacts
			}
			switch allocation.Kind {
			case "payment_invoice", "credit_application", "refund_credit":
			default:
				return errFinancialExportFacts
			}
			child := append([]string{}, values...)
			child[2] = "allocation"
			child[16], child[17], child[18] = optionalExportID(allocation.InvoiceID), allocation.CreditID, allocation.PaymentID
			child[19], child[20], child[21] = allocation.Kind, allocation.ID, allocation.SourceDate
			child[22], child[23], child[24], child[25] = "", "", "", financialMoney(allocation.AmountCents)
			if err := emitFinancialRecord(report, child, false, refs, emit, provenance); err != nil {
				return err
			}
		}
		total := totals[event.Kind]
		for _, pair := range []struct {
			target *int64
			value  int64
		}{{&total.count, 1}, {&total.amount, event.AmountCents}, {&total.cash, event.NetPaymentsCents}, {&totalCount, 1}, {&totalCash, event.NetPaymentsCents}} {
			if err := financialSum(pair.target, pair.value); err != nil {
				return err
			}
		}
		totals[event.Kind] = total
	}
	refs := financialReportReferences(report)
	for _, kind := range financialEventTypes {
		total := totals[kind]
		values := financialActivityValues(report, "event_total", section)
		values[9], values[22], values[23], values[24] = kind, strconv.FormatInt(total.count, 10), financialMoney(total.amount), financialMoney(total.cash)
		if err := emitFinancialRecord(report, values, true, refs, emit); err != nil {
			return err
		}
	}
	values := financialActivityValues(report, "net_payments_total", section)
	values[22], values[24] = strconv.FormatInt(totalCount, 10), financialMoney(totalCash)
	return emitFinancialRecord(report, values, true, refs, emit)
}

// Additional source identities stay within the settlement facet. A Credit used
// by an allocation is checked as historical creation, never as current Credit.
func financialActivityReferences(event services.FinancialActivityRow) []authorization.FinancialReference {
	refs := []authorization.FinancialReference{event.Reference}
	add := func(kind, id string) {
		if id != "" {
			refs = append(refs, authorization.FinancialReference{Facet: authorization.FinancialSettlement, CustomerID: event.CustomerID, Archived: event.Archived, EventKind: kind, EventID: id})
		}
	}
	add(event.OriginalKind, event.OriginalID)
	add("payment", event.PaymentID)
	add("credit_creation", event.CreditID)
	for _, allocation := range event.Allocations {
		add("payment", allocation.PaymentID)
		add("credit_creation", allocation.CreditID)
	}
	return refs
}
