package services

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/ent"
)

// RenderPDFSource renders only accepted capture values. It performs no database
// or filesystem reads, including for branding; callers publish through the
// authorization service's source comparison and atomic audit transaction.
func RenderPDFSource(kind string, source authorization.PDFSource) (string, []byte, error) {
	if source.LogoUnavailable {
		return "", nil, fmt.Errorf("captured company logo is unavailable")
	}
	var capture struct {
		Document json.RawMessage
		Customer *ent.Customer
		Job      json.RawMessage
		Asset    *ent.Asset
		Status   *ent.Status
	}
	if err := json.Unmarshal(source.Snapshot, &capture); err != nil {
		return "", nil, err
	}
	var job *ent.Job
	if len(capture.Job) > 0 && string(capture.Job) != "null" {
		job = new(ent.Job)
		if err := decodePDFRecord(capture.Job, job); err != nil {
			return "", nil, err
		}
	}
	var statuses []*ent.Status
	if capture.Status != nil {
		statuses = append(statuses, capture.Status)
	}
	var output bytes.Buffer
	var number string
	var err error
	switch kind {
	case "invoice":
		var invoice ent.Invoice
		if err = decodePDFRecord(capture.Document, &invoice); err != nil {
			return "", nil, err
		}
		if capture.Status != nil {
			invoice.StatusID = &capture.Status.ID
		}
		totals, e := FinancialTotals(invoice.Financial, invoice.LineItems, invoice.TaxRate)
		if e != nil {
			return "", nil, e
		}
		if source.SettledCents > totals.Total.MinorUnits() {
			return "", nil, fmt.Errorf("invalid captured settlement")
		}
		number = invoice.NumberLabel
		err = generateInvoicePDF(&output, &invoice, capture.Customer, job, capture.Asset, statuses, source.Settings, InvoicePDFSettlement{AllocatedCents: source.AllocatedCents, AppliedCents: source.AppliedCents, AmountPaidCents: source.SettledCents, AmountDueCents: totals.Total.MinorUnits() - source.SettledCents}, &source.Logo, source.FieldLines...)
	case "estimate":
		var estimate ent.Estimate
		if err = decodePDFRecord(capture.Document, &estimate); err != nil {
			return "", nil, err
		}
		number = estimate.NumberLabel
		err = generateEstimatePDF(&output, &estimate, capture.Customer, job, statuses, source.Settings, &source.Logo, source.FieldLines...)
	default:
		return "", nil, authorization.ErrNotFound
	}
	if err != nil {
		return "", nil, err
	}
	return number + ".pdf", output.Bytes(), nil
}

// SQL calendar dates remain dates in the retained source. Ent represents them as
// time.Time; adapt only the renderer input, never the captured history.
func decodePDFRecord(raw []byte, target any) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if items, ok := fields["line_items"]; ok {
		fields["line_items"], _ = json.Marshal(string(items))
	}
	for _, key := range []string{"invoice_date", "due_date", "valid_through"} {
		var date string
		if json.Unmarshal(fields[key], &date) == nil && len(date) == 10 {
			fields[key], _ = json.Marshal(date + "T00:00:00Z")
		}
	}
	normalized, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	return json.Unmarshal(normalized, target)
}
