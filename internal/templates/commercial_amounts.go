package templates

import (
	"encoding/json"
	"fmt"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/freefsm-project/freefsm/internal/services"
	"math/big"
)

func documentAmount(items []services.LineItem, rate, financial, part string) string {
	raw, err := services.EncodeCommercialLines(items)
	if err != nil {
		return "Unavailable"
	}
	t, err := services.FinancialTotals(financial, raw, rate)
	if err != nil {
		return "Unavailable"
	}
	switch part {
	case "tax":
		return t.Tax.Format()
	case "subtotal":
		return t.Subtotal.Format()
	default:
		return t.Total.Format()
	}
}
func documentLineAmount(li services.LineItem, financial string, index int) string {
	if financial != "" {
		s, err := commercial.Read(financial)
		if err != nil || index >= len(s.Result.Lines) {
			return "Unavailable"
		}
		return commercial.FormatCents(s.Result.Lines[index].AdjustedCents)
	}
	m, err := services.LineAmount(li)
	if err != nil {
		return "Unavailable"
	}
	return m.Format()
}
func commercialQuantity(li services.LineItem) string {
	text := services.LineQuantityText(li)
	value, ok := new(big.Rat).SetString(text)
	if !ok || value.Sign() <= 0 {
		return "Unavailable"
	}
	return text
}
func commercialPrice(li services.LineItem) string {
	text := services.LineUnitPriceText(li)
	if _, ok := new(big.Rat).SetString(text); !ok {
		return "Unavailable"
	}
	return text
}

// A missing calculation is not a monetary value. Keep it visible without a
// currency prefix, while valid zero and signed amounts retain their formatting.
func commercialCurrency(value string) string {
	if value == "Unavailable" {
		return value
	}
	return "$" + value
}
func estimateFinancial(d *EstimateDetail, items string, existing bool) string {
	if d == nil {
		return ""
	}
	if d.Financial != "" || !existing {
		return d.Financial
	}
	return legacyEditorPreview(items, d.TaxRate)
}
func invoiceFinancial(d *InvoiceDetail, items string, existing bool) string {
	if d == nil {
		return ""
	}
	if d.Financial != "" || !existing {
		return d.Financial
	}
	return legacyEditorPreview(items, d.TaxRate)
}

// This is a transient legacy display projection, never an accepted snapshot or
// a migration. It avoids reinterpreting unchanged historical editor amounts.
func legacyEditorPreview(raw, rate string) string {
	lines, err := services.DecodeLineItems(raw)
	if err != nil {
		return ""
	}
	totals, err := services.CalculateTotals(lines, rate)
	if err != nil {
		return ""
	}
	result := commercial.Result{PretaxCents: totals.Subtotal.MinorUnits(), TaxCents: totals.Tax.MinorUnits(), TotalCents: totals.Total.MinorUnits(), Lines: []commercial.LineResult{}}
	for _, line := range lines {
		amount, err := services.LineAmount(line)
		if err != nil {
			return ""
		}
		result.Lines = append(result.Lines, commercial.LineResult{AdjustedCents: amount.MinorUnits()})
	}
	encoded, err := json.Marshal(map[string]any{"legacy_preview": true, "result": result})
	if err != nil {
		return ""
	}
	return string(encoded)
}

func dashboardAmount(exact string, fallback float64) string {
	if exact != "" {
		return "$" + exact
	}
	return fmt.Sprintf("$%.2f", fallback)
}

func exactCents(c int64) string { return "$" + commercial.FormatCents(c) }

func documentAdjustmentSummary(raw string) string       { return services.FinancialAdjustmentText(raw) }
func lineAdjustmentSummary(li services.LineItem) string { return services.LineAdjustmentText(li) }

func commercialInputType(job bool) string {
	if job {
		return "number"
	}
	return "text"
}
