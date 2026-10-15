package handlers

import (
	"encoding/json"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/services"
	"io"
	"math/big"
	"net/http"
	"strings"
)

func decodeAndValidateLineItems(raw, taxRate string) ([]services.LineItem, error) {
	items, err := services.DecodeCommercialLines(raw)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []services.LineItem{}
	}
	in, err := services.FinancialInput(items, taxRate, services.DocumentAdjustments{})
	if err != nil {
		return nil, err
	}
	rate, ok := new(big.Rat).SetString(in.TaxRate)
	if !ok || rate.Sign() < 0 || rate.Cmp(big.NewRat(100, 1)) > 0 {
		return nil, authorization.ErrCommercialInput
	}
	for _, line := range in.Lines {
		q, ok := new(big.Rat).SetString(line.Quantity)
		if !ok || q.Sign() <= 0 {
			return nil, authorization.ErrCommercialInput
		}
	}
	return items, nil
}

func documentAdjustments(r *http.Request) (*services.DocumentAdjustments, error) {
	raw := r.FormValue("adjustments")
	if raw == "" || strings.TrimSpace(raw) == "null" {
		return nil, nil
	}
	var a services.DocumentAdjustments
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&a); err != nil {
		return nil, authorization.ErrCommercialInput
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, authorization.ErrCommercialInput
	}
	return &a, nil
}

func submittedAdjustments(raw string) string {
	var a services.DocumentAdjustments
	if json.Unmarshal([]byte(raw), &a) != nil {
		return "{}"
	}
	b, _ := json.Marshal(a)
	return string(b)
}
