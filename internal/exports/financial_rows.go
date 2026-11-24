package exports

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
)

var errFinancialExportFacts = errors.New("invalid financial CSV snapshot facts")

func financialMoney(cents int64) string {
	sign := ""
	var magnitude uint64
	if cents < 0 {
		sign = "-"
		magnitude = uint64(-(cents + 1)) + 1
	} else {
		magnitude = uint64(cents)
	}
	return fmt.Sprintf("%s%d.%02d", sign, magnitude/100, magnitude%100)
}

func financialSum(target *int64, value int64) error {
	if value > 0 && *target > math.MaxInt64-value || value < 0 && *target < math.MinInt64-value {
		return errors.New("financial CSV total exceeds exact cents range")
	}
	*target += value
	return nil
}

func financialPrefix(report services.FinancialReport, rowType string) []string {
	return []string{report.AsOf.UTC().Format(time.RFC3339Nano), report.Timezone, rowType}
}

func emitFinancialRecord(report services.FinancialReport, values []string, summary bool, refs []authorization.FinancialReference, emit func(Record) error, provenance ...financialProvenance) error {
	if summary {
		// Totals bind the same immutable source/allocation tuples as detail rows.
		// Their references must stand on their own, not rely on adjacent rows.
		provenance = financialReportProvenance(report)
	}
	raw, err := financialReferenceBytes(report.Kind, summary, refs, provenance...)
	if err != nil {
		return err
	}
	return emit(Record{Values: values, Reference: raw})
}

func financialReportReferences(report services.FinancialReport) []authorization.FinancialReference {
	// Include typed-row references too: a summary must not lose a contributor if
	// a selector omits its redundant report-level copy.
	refs := append([]authorization.FinancialReference{}, report.References...)
	for _, r := range report.Invoices {
		refs = append(refs, r.Reference)
	}
	for _, r := range report.Customers {
		refs = append(refs, r.Reference)
	}
	for _, r := range report.Activity {
		refs = append(refs, financialActivityReferences(r)...)
	}
	for _, r := range report.UnknownActivity {
		refs = append(refs, financialActivityReferences(r)...)
	}
	for _, r := range report.Credits {
		refs = append(refs, r.Reference)
	}
	return refs
}

func emitFinancialReport(report services.FinancialReport, emit func(Record) error) error {
	switch report.Kind {
	case authorization.FinancialAging:
		return emitFinancialAging(report, emit)
	case authorization.FinancialCustomers:
		return emitFinancialCustomers(report, emit)
	case authorization.FinancialSettlement:
		return emitFinancialActivity(report, emit)
	case authorization.FinancialCredit:
		return emitFinancialCredit(report, emit)
	case authorization.FinancialCombined:
		if err := emitFinancialActivity(report, emit); err != nil {
			return err
		}
		return emitFinancialCredit(report, emit)
	default:
		return ErrConflict
	}
}

func emitFinancialAging(report services.FinancialReport, emit func(Record) error) error {
	for _, row := range report.Invoices {
		values := append(financialPrefix(report, "invoice"), strconv.FormatInt(row.CustomerID, 10), row.Customer, strconv.FormatInt(row.ID, 10), row.Number, row.DueDate, row.Bucket, "1", financialMoney(row.TotalCents), financialMoney(row.SettledCents), financialMoney(row.DueCents))
		if err := emitFinancialRecord(report, values, false, []authorization.FinancialReference{row.Reference}, emit); err != nil {
			return err
		}
	}
	refs := financialReportReferences(report)
	emitTotal := func(rowType, bucket string, t services.FinancialAgingTotal) error {
		values := append(financialPrefix(report, rowType), "", "", "", "", "", bucket, strconv.Itoa(t.InvoiceCount), financialMoney(t.TotalCents), financialMoney(t.SettledCents), financialMoney(t.ReceivableCents))
		return emitFinancialRecord(report, values, true, refs, emit)
	}
	for i, bucket := range services.FinancialAgingBuckets {
		if report.Filter.Bucket == "" || report.Filter.Bucket == bucket {
			if err := emitTotal("bucket_total", bucket, report.Totals.BucketTotals[i]); err != nil {
				return err
			}
		}
	}
	return emitTotal("grand_total", "", services.FinancialAgingTotal{InvoiceCount: report.Totals.InvoiceCount, TotalCents: report.Totals.TotalCents, SettledCents: report.Totals.SettledCents, ReceivableCents: report.Totals.ReceivableCents})
}

func emitFinancialCustomers(report services.FinancialReport, emit func(Record) error) error {
	for _, row := range report.Customers {
		values := append(financialPrefix(report, "customer"), strconv.FormatInt(row.ID, 10), row.Name, strconv.Itoa(row.InvoiceCount), financialMoney(row.TotalCents), financialMoney(row.SettledCents), financialMoney(row.ReceivableCents))
		for _, amount := range row.Buckets {
			values = append(values, financialMoney(amount))
		}
		if err := emitFinancialRecord(report, values, false, []authorization.FinancialReference{row.Reference}, emit); err != nil {
			return err
		}
	}
	values := append(financialPrefix(report, "grand_total"), "", "", strconv.Itoa(report.Totals.InvoiceCount), financialMoney(report.Totals.TotalCents), financialMoney(report.Totals.SettledCents), financialMoney(report.Totals.ReceivableCents))
	for _, amount := range report.Totals.Buckets {
		values = append(values, financialMoney(amount))
	}
	return emitFinancialRecord(report, values, true, financialReportReferences(report), emit)
}

func financialInstant(t *time.Time) (string, string) {
	if t == nil {
		return "", "false"
	}
	return t.UTC().Format(time.RFC3339Nano), "true"
}

func emitFinancialCredit(report services.FinancialReport, emit func(Record) error) error {
	for _, row := range report.Credits {
		entered, known := financialInstant(row.RecordedAt)
		var values []string
		if report.Kind == authorization.FinancialCombined {
			values = make([]string, len(financialCombinedColumns()))
			copy(values, financialPrefix(report, "credit_source"))
			values[3], values[4], values[5] = "current_credit", report.Filter.From, report.Filter.To
			values[6], values[7], values[8] = strconv.FormatInt(row.CustomerID, 10), row.Customer, strconv.FormatBool(row.Archived)
			values[17], values[18], values[21] = row.ID, row.PaymentID, row.SourceDate
			values[26], values[27], values[28], values[29] = entered, known, financialMoney(row.OriginalCents), financialMoney(row.AvailableCents)
		} else {
			values = append(financialPrefix(report, "credit_source"), strconv.FormatInt(row.CustomerID, 10), row.Customer, strconv.FormatBool(row.Archived), row.ID, row.PaymentID, row.SourceDate, entered, known, financialMoney(row.OriginalCents), financialMoney(row.AvailableCents))
		}
		provenance := financialProvenance{Facet: authorization.FinancialCredit, CustomerID: row.CustomerID, Kind: "credit_source", ID: row.ID, CreditID: row.ID, PaymentID: row.PaymentID}
		if err := emitFinancialRecord(report, values, false, []authorization.FinancialReference{row.Reference}, emit, provenance); err != nil {
			return err
		}
	}
	var values []string
	if report.Kind == authorization.FinancialCombined {
		values = make([]string, len(financialCombinedColumns()))
		copy(values, financialPrefix(report, "current_credit_total"))
		values[3], values[4], values[5], values[29] = "current_credit", report.Filter.From, report.Filter.To, financialMoney(report.Totals.AvailableCreditCents)
	} else {
		values = append(financialPrefix(report, "current_credit_total"), "", "", "", "", "", "", "", "", "", financialMoney(report.Totals.AvailableCreditCents))
	}
	return emitFinancialRecord(report, values, true, financialReportReferences(report), emit)
}
