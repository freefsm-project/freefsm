package commercial

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
)

// CapturedInput reads existing line capture storage without passing decimal
// inputs through float64. It does not calculate or reinterpret historical reads;
// callers use it only for a new document or a reviewed monetary/Customer edit.
func CapturedInput(raw, rate string, discount, surcharge *Adjustment) (Input, error) {
	var lines []struct {
		Title               string      `json:"title"`
		Price               json.Number `json:"unit_price"`
		Quantity            json.Number `json:"quantity"`
		ExactPrice          string      `json:"unit_price_decimal"`
		ExactQuantity       string      `json:"quantity_decimal"`
		Taxable             bool        `json:"taxable"`
		Discount            json.Number `json:"discount"`
		Surcharge           json.Number `json:"surcharge"`
		DiscountAdjustment  *Adjustment `json:"discount_adjustment"`
		SurchargeAdjustment *Adjustment `json:"surcharge_adjustment"`
	}
	if err := json.Unmarshal([]byte(raw), &lines); err != nil {
		return Input{}, ErrInvalid
	}
	rate = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rate), "%"))
	if rate == "" {
		rate = "0"
	}
	in := Input{Lines: make([]Line, 0, len(lines)), TaxRate: rate, Discount: discount, Surcharge: surcharge}
	money := func(s string) string {
		if s == "" {
			return "0.00"
		}
		parts := strings.Split(s, ".")
		if len(parts) == 1 {
			return s + ".00"
		}
		if len(parts) == 2 && len(parts[1]) == 1 {
			return s + "0"
		}
		return s
	}
	adjustment := func(typed *Adjustment, legacy json.Number) (*Adjustment, error) {
		if legacy == "" {
			return typed, nil
		}
		text := capturedDecimal(legacy.String())
		v, err := decimal(text)
		if err != nil {
			return nil, err
		}
		if v.Sign() == 0 {
			return typed, nil
		}
		if typed != nil {
			return nil, ErrInvalid
		}
		return &Adjustment{Kind: "fixed", Value: money(text)}, nil
	}
	for _, capture := range lines {
		if strings.TrimSpace(capture.Title) == "" {
			return Input{}, ErrInvalidLine
		}
		price, quantity := capture.ExactPrice, capture.ExactQuantity
		if price == "" {
			price = money(capturedDecimal(capture.Price.String()))
		}
		if quantity == "" {
			quantity = capturedDecimal(capture.Quantity.String())
		}
		d, err := adjustment(capture.DiscountAdjustment, capture.Discount)
		if err != nil {
			return Input{}, err
		}
		s, err := adjustment(capture.SurchargeAdjustment, capture.Surcharge)
		if err != nil {
			return Input{}, err
		}
		in.Lines = append(in.Lines, Line{Quantity: quantity, UnitPrice: price, Taxable: capture.Taxable, Discount: d, Surcharge: s})
	}
	return in, nil
}

func capturedDecimal(text string) string {
	index := strings.IndexAny(text, "eE")
	if index < 0 {
		return text
	}
	exponent, err := strconv.Atoi(text[index+1:])
	if err != nil || exponent < -400 || exponent > 400 {
		return text
	}
	precision := 0
	if point := strings.IndexByte(text[:index], '.'); point >= 0 {
		precision = index - point - 1
	}
	precision -= exponent
	if precision < 0 {
		precision = 0
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return text
	}
	return value.FloatString(precision)
}
