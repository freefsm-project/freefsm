package v1

import (
	"fmt"

	"github.com/freefsm-project/freefsm/internal/services"
)

// jobPricingLineDTO is the public, Cost-free projection of a captured line.
// USD amounts are strings; quantities and percentage rates retain their units.
// Keep it separate from the persisted snapshot and its arithmetic inputs.
type jobPricingLineDTO struct {
	SnapshotID  string  `json:"snapshot_id,omitempty"`
	ItemID      int64   `json:"item_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	UnitPrice   string  `json:"unit_price"`
	Quantity    float64 `json:"quantity"`
	Taxable     bool    `json:"taxable"`
	TaxRate     string  `json:"tax_rate"`
	Discount    string  `json:"discount"`
	Surcharge   string  `json:"surcharge"`
}

func publicJobPricing(jobID int64, raw string) ([]jobPricingLineDTO, error) {
	lines, err := services.JobPricingLines(jobID, raw)
	if err != nil {
		return nil, err
	}
	out := make([]jobPricingLineDTO, 0, len(lines))
	for _, line := range lines {
		price, err := jobPricingUSD(line.UnitPrice)
		if err != nil {
			return nil, err
		}
		discount, err := jobPricingUSD(line.Discount)
		if err != nil {
			return nil, err
		}
		surcharge, err := jobPricingUSD(line.Surcharge)
		if err != nil {
			return nil, err
		}
		out = append(out, jobPricingLineDTO{
			SnapshotID: line.SnapshotID, ItemID: line.ItemID,
			Title: line.Title, Description: line.Description,
			UnitPrice: price, Quantity: line.Quantity,
			Taxable: line.Taxable, TaxRate: line.TaxRate,
			Discount: discount, Surcharge: surcharge,
		})
	}
	return out, nil
}

func jobPricingUSD(value float64) (string, error) {
	// Use the existing decimal half-away-from-zero rounding rule, then format
	// integer cents directly rather than reintroducing binary floating point.
	money, err := services.MoneyFromMajorUnits(value)
	if err != nil {
		return "", err
	}
	cents := money.MinorUnits()
	if cents < 0 {
		return fmt.Sprintf("-%d.%02d", -(cents / 100), -(cents % 100)), nil
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100), nil
}
