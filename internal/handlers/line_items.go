package handlers

import "github.com/freefsm-project/freefsm/internal/services"

func decodeAndValidateLineItems(raw, taxRate string) ([]services.LineItem, error) {
	items, err := services.DecodeLineItems(raw)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []services.LineItem{}
	}
	if _, err := services.CalculateTotals(items, taxRate); err != nil {
		return nil, err
	}
	return items, nil
}
