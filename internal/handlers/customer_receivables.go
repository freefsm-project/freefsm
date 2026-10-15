package handlers

import (
	"context"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/freefsm-project/freefsm/internal/templates"
)

func customerReceivables(ctx context.Context, request authorization.Request, id int64, loc *time.Location) (templates.CustomerReceivables, error) {
	result := templates.CustomerReceivables{Buckets: []templates.ReceivableBucket{{Label: "Current"}, {Label: "1–30"}, {Label: "31–60"}, {Label: "61–90"}, {Label: "91+"}, {Label: "No Due Date"}}}
	rows, err := request.Service.CustomerReceivableSources(ctx, request.Actor, id)
	if err != nil {
		return result, err
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for _, row := range rows {
		totals, err := services.FinancialTotals(row.Financial, row.LineItems, row.TaxRate)
		if err != nil {
			return result, err
		}
		due := totals.Total.MinorUnits() - row.SettledCents
		if due <= 0 {
			continue
		}
		bucket := 5
		if row.DueDate != nil && !row.DueDate.IsZero() {
			// Stored due dates are calendar values, not elapsed 24-hour instants.
			d := row.DueDate.In(loc)
			date := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
			days := int(today.Sub(date) / (24 * time.Hour))
			switch {
			case days <= 0:
				bucket = 0
			case days <= 30:
				bucket = 1
			case days <= 60:
				bucket = 2
			case days <= 90:
				bucket = 3
			default:
				bucket = 4
			}
		}
		result.Buckets[bucket].Cents += due
		result.Buckets[bucket].Count++
		result.TotalCents += due
		result.Count++
	}
	return result, nil
}
