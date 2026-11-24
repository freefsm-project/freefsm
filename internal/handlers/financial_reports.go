package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
	"github.com/go-chi/chi/v5"
)

var financialBucketLabels = [6]string{"Not yet due", "1–30", "31–60", "61–90", "91+", "No due date"}

func registerFinancialReports(r chi.Router) {
	r.Get(agingReportPath, financialReport)
	r.Get(paymentsCreditsReportPath, financialReport)
}

func financialReportFields(kind authorization.FinancialReportKind) []templates.WorkReportField {
	fields := []templates.WorkReportField{{Name: "customer_id", Label: "Customer ID", Type: "number"}}
	if kind == authorization.FinancialAging {
		options := reportOptions("", "All buckets")
		for i, bucket := range services.FinancialAgingBuckets {
			options = append(options, templates.WorkReportOption{Value: bucket, Label: financialBucketLabels[i]})
		}
		fields = append(fields, templates.WorkReportField{Name: "bucket", Label: "Aging bucket", Options: options})
	}
	if kind == authorization.FinancialSettlement || kind == authorization.FinancialCombined {
		fields = append(fields, templates.WorkReportField{Name: "from", Label: "Recorded from (inclusive company date)", Type: "date"}, templates.WorkReportField{Name: "to", Label: "Recorded through (inclusive company date)", Type: "date"})
	}
	if kind == authorization.FinancialSettlement || kind == authorization.FinancialCombined || kind == authorization.FinancialCredit {
		fields = append(fields, templates.WorkReportField{Name: "include_archived", Label: "Customer history", Options: reportOptions("false", "Active Customers", "true", "Include authorized archived Customers")})
	}
	return fields
}

func financialReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "Invalid report filters", http.StatusBadRequest)
		return
	}
	p := templates.FinancialReportPageData{Title: "Financial report", Path: r.URL.Path, Filters: q}
	intent, err := financialRequestedView(r.URL.Path, q)
	if err != nil {
		financialReportError(w, r, p, err)
		return
	}
	if intent.Value != "" {
		p.Title, p.View, p.Fields = intent.Label, intent.Value, financialReportFields(intent.Kind)
		p.Views = []templates.WorkReportOption{{Value: intent.Value, Label: intent.Label}}
	}
	view, options, err := financialView(r, q)
	if err != nil {
		financialReportError(w, r, p, err)
		return
	}
	p.Title, p.View, p.Views, p.Fields = view.Label, view.Value, options, financialReportFields(view.Kind)
	p.Description = "Authorized current snapshot in USD. Receivables do not subtract Credit. Activity uses recording time; current Credit is independent of activity dates. Archived history is read-only."
	f, err := parseFinancialReportFilter(view.Kind, q)
	if err != nil {
		financialReportError(w, r, p, err)
		return
	}
	p.Filters = financialFilterValues(view.Kind, f)
	auth, _ := authorization.FromContext(r.Context())
	report, err := services.ReadFinancialReport(r.Context(), auth.Service.Pool, auth.Actor, view.Kind, f)
	if err != nil {
		financialReportError(w, r, p, err)
		return
	}
	p.Filters = financialFilterValues(view.Kind, report.Filter)
	p.UpdatedAt, p.Timezone = snapshotStamp(report.AsOf, report.Timezone), report.Timezone
	p.Sections = financialSections(report)
	principal, err := auth.Service.Principal(r.Context(), auth.Actor)
	if err != nil {
		financialReportError(w, r, p, err)
		return
	}
	p.Exports = financialExportControls(principal, report)
	templates.FinancialReport(p).Render(r.Context(), w)
}

func financialReportError(w http.ResponseWriter, r *http.Request, p templates.FinancialReportPageData, err error) {
	if errors.Is(err, authorization.ErrForbidden) || errors.Is(err, authorization.ErrNotFound) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	status := http.StatusServiceUnavailable
	p.Error = "The report could not be loaded. Your filters are retained; retry to obtain a fresh snapshot."
	if errors.Is(err, services.ErrFinancialReportFilter) || errors.Is(err, errFinancialReportView) {
		status = http.StatusBadRequest
		p.Error = "Invalid report filters. Correct the filters and retry."
	} else {
		slog.Warn("financial report unavailable", "path", r.URL.Path, "error", err)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	templates.FinancialReport(p).Render(r.Context(), w)
}

// Format signed integer cents without floating point or negating MinInt64.
func financialMoney(value int64) string {
	sign := ""
	var magnitude uint64
	if value < 0 {
		sign = "-"
		magnitude = uint64(-(value + 1)) + 1
	} else {
		magnitude = uint64(value)
	}
	return fmt.Sprintf("%s$%d.%02d", sign, magnitude/100, magnitude%100)
}

func financialCells(values ...string) []templates.FinancialCell {
	var out []templates.FinancialCell
	for _, value := range values {
		out = append(out, templates.FinancialCell{Text: value})
	}
	return out
}

func financialKnown(value string) templates.FinancialCell {
	return templates.FinancialCell{Text: value, Unknown: value == ""}
}

func financialCustomerName(name string, archived bool) string {
	if archived {
		return name + " (archived — read-only)"
	}
	return name
}

func financialSections(report services.FinancialReport) []templates.FinancialSection {
	var sections []templates.FinancialSection
	if report.Kind == authorization.FinancialAging || report.Kind == authorization.FinancialCustomers {
		s := templates.FinancialSection{Title: "Current receivables", Description: "Eligible active issued, non-Void, positive-due Invoices only. Available Credit is not subtracted.", Measures: []templates.WorkReportMeasure{{Label: "Outstanding", Value: financialMoney(report.Totals.ReceivableCents)}, {Label: "Eligible Invoices", Value: strconv.Itoa(report.Totals.InvoiceCount)}}}
		for i, label := range financialBucketLabels {
			s.Measures = append(s.Measures, templates.WorkReportMeasure{Label: label, Value: financialMoney(report.Totals.Buckets[i])})
		}
		if report.Kind == authorization.FinancialAging {
			s.Headers = []string{"Invoice", "Customer", "Due date", "Bucket", "Total", "Settled", "Outstanding"}
			for _, row := range report.Invoices {
				due := row.DueDate
				if due == "" {
					due = "No due date"
				}
				bucket := row.Bucket
				for i, key := range services.FinancialAgingBuckets {
					if key == bucket {
						bucket = financialBucketLabels[i]
						break
					}
				}
				cells := financialCells(row.Number+" — "+row.Title, row.Customer, due, bucket, financialMoney(row.TotalCents), financialMoney(row.SettledCents), financialMoney(row.DueCents))
				cells[0].URL = fmt.Sprintf("/invoices/%d", row.ID)
				s.Rows = append(s.Rows, templates.FinancialRow{Cells: cells})
			}
		} else {
			s.Headers = append([]string{"Customer", "Eligible Invoices", "Outstanding"}, financialBucketLabels[:]...)
			for _, row := range report.Customers {
				cells := financialCells(row.Name, strconv.Itoa(row.InvoiceCount), financialMoney(row.ReceivableCents))
				for _, amount := range row.Buckets {
					cells = append(cells, templates.FinancialCell{Text: financialMoney(amount)})
				}
				s.Rows = append(s.Rows, templates.FinancialRow{Cells: cells})
			}
		}
		sections = append(sections, s)
	}
	if report.Kind == authorization.FinancialSettlement || report.Kind == authorization.FinancialCombined {
		known := financialActivitySection("Dated activity", "Recording-time activity for the selected inclusive company dates. Net Payments = Payments − Payment reversals − refunds + refund reversals. Applications and Credit creation have no cash effect.", report.Activity, report.Timezone)
		known.Measures = []templates.WorkReportMeasure{{Label: "Net Payments (dated activity)", Value: financialMoney(report.Totals.NetPaymentsCents)}}
		unknown := financialActivitySection("Unknown recording-time history", "These events cannot be attributed to the selected period. They remain visible separately and are excluded from dated totals; effective dates do not establish recording time.", report.UnknownActivity, report.Timezone)
		unknown.Measures = []templates.WorkReportMeasure{{Label: "Unknown-time events", Value: strconv.Itoa(report.Totals.UnknownEventCount)}, {Label: "Unattributed cash effect (outside dated totals)", Value: financialMoney(report.Totals.UnknownNetPaymentsCents)}, {Label: "Event face amounts (not cash or balance)", Value: financialMoney(report.Totals.UnknownAmountCents)}}
		sections = append(sections, known, unknown)
	}
	if report.Kind == authorization.FinancialCredit || report.Kind == authorization.FinancialCombined {
		s := templates.FinancialSection{Title: "Current Available Credit", Description: "Current positive sources at this snapshot, independent of the activity period. Source funding is provenance, not an additional balance.", Headers: []string{"Credit ID", "Customer", "Source Payment ID", "Source effective date", "Original funding", "Current available"}, Measures: []templates.WorkReportMeasure{{Label: "Available Credit", Value: financialMoney(report.Totals.AvailableCreditCents)}}}
		for _, row := range report.Credits {
			cells := financialCells(row.ID, financialCustomerName(row.Customer, row.Archived), row.PaymentID, row.SourceDate, financialMoney(row.OriginalCents), financialMoney(row.AvailableCents))
			cells[3] = financialKnown(row.SourceDate)
			s.Rows = append(s.Rows, templates.FinancialRow{Cells: cells})
		}
		sections = append(sections, s)
	}
	return sections
}

func financialActivitySection(title, description string, rows []services.FinancialActivityRow, zone string) templates.FinancialSection {
	s := templates.FinancialSection{Title: title, Description: description, Headers: []string{"Event", "Event ID", "Customer", "Recorded at", "Effective date", "Original event", "Payment ID", "Credit ID", "Invoice identity (not a link)", "Face amount", "Cash effect"}}
	for _, row := range rows {
		invoice := ""
		if row.InvoiceID != 0 {
			invoice = strconv.FormatInt(row.InvoiceID, 10)
		}
		cells := financialCells(row.Kind, row.ID, financialCustomerName(row.Customer, row.Archived), "", row.EffectiveDate, row.OriginalKind+" "+row.OriginalID, row.PaymentID, row.CreditID, invoice, financialMoney(row.AmountCents), financialMoney(row.NetPaymentsCents))
		cells[3].Unknown = row.RecordedAt == nil
		if row.RecordedAt != nil {
			cells[3].Text = snapshotStamp(*row.RecordedAt, zone)
		}
		cells[4] = financialKnown(row.EffectiveDate)
		result := templates.FinancialRow{Cells: cells}
		for _, source := range row.Allocations {
			date := source.SourceDate
			if date == "" {
				date = "Unknown"
			}
			invoice := ""
			if source.InvoiceID != 0 {
				invoice = strconv.FormatInt(source.InvoiceID, 10)
			}
			result.Allocations = append(result.Allocations, templates.FinancialSourcePortion{Kind: source.Kind, ID: source.ID, InvoiceID: invoice, CreditID: source.CreditID, PaymentID: source.PaymentID, SourceDate: date, Amount: financialMoney(source.AmountCents)})
		}
		s.Rows = append(s.Rows, result)
	}
	return s
}
