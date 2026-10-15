package services

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/commercial"
	"github.com/freefsm-project/freefsm/internal/ent"
	"github.com/jackc/pgx/v5"
)

type DocumentAdjustments struct {
	Discount  *commercial.Adjustment `json:"discount,omitempty"`
	Surcharge *commercial.Adjustment `json:"surcharge,omitempty"`
}

func FinancialInput(items []LineItem, rate string, adjustments DocumentAdjustments) (commercial.Input, error) {
	raw, err := json.Marshal(items)
	if err != nil {
		return commercial.Input{}, errors.Join(authorization.ErrCommercialInput, ErrInvalidLineItem, err)
	}
	in, err := commercial.CapturedInput(string(raw), rate, adjustments.Discount, adjustments.Surcharge)
	if err != nil {
		return in, commercialInputError(err)
	}
	return in, nil
}

// DecodeCommercialLines retains decimal tokens from browser and captured Job /
// document JSON. The legacy decoder remains unchanged for historical arithmetic.
func DecodeCommercialLines(raw string) ([]LineItem, error) {
	items, err := DecodeLineItems(raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		raw = "[]"
	}
	input, err := commercial.CapturedInput(raw, "0", nil, nil)
	if err != nil {
		return nil, commercialInputError(err)
	}
	for index := range items {
		items[index].ExactUnitPrice = input.Lines[index].UnitPrice
		items[index].ExactQuantity = input.Lines[index].Quantity
		items[index].DiscountAdjustment = input.Lines[index].Discount
		items[index].SurchargeAdjustment = input.Lines[index].Surcharge
		items[index].Discount, items[index].Surcharge = 0, 0
	}
	return items, nil
}

func decimalMoney(s string) string {
	parts := strings.Split(s, ".")
	if len(parts) == 1 {
		return s + ".00"
	}
	if len(parts) == 2 && len(parts[1]) == 1 {
		return s + "0"
	}
	return s
}

func LineUnitPriceText(line LineItem) string {
	if line.ExactUnitPrice != "" {
		return line.ExactUnitPrice
	}
	return decimalMoney(strconv.FormatFloat(line.UnitPrice, 'f', -1, 64))
}
func LineQuantityText(line LineItem) string {
	if line.ExactQuantity != "" {
		return line.ExactQuantity
	}
	return strconv.FormatFloat(line.Quantity, 'f', -1, 64)
}

func adjustmentText(name string, a *commercial.Adjustment) string {
	if a == nil {
		return ""
	}
	value := "$" + a.Value
	if a.Kind == "percentage" {
		value = a.Value + "%"
	}
	return name + ": " + value
}

func FinancialAdjustmentText(raw string) string {
	a, err := FinancialAdjustments(raw)
	if err != nil {
		return "Recorded adjustments unavailable"
	}
	parts := []string{}
	if s := adjustmentText("Document discount", a.Discount); s != "" {
		parts = append(parts, s)
	}
	if s := adjustmentText("Document surcharge", a.Surcharge); s != "" {
		parts = append(parts, s)
	}
	if raw != "" {
		snapshot, err := commercial.Read(raw)
		if err == nil && snapshot.Input.TaxExempt {
			parts = append(parts, "Customer tax exemption applied")
		}
	}
	return strings.Join(parts, "; ")
}

func LineAdjustmentText(li LineItem) string {
	d, s := li.DiscountAdjustment, li.SurchargeAdjustment
	if d == nil && li.Discount != 0 {
		d = &commercial.Adjustment{Kind: "fixed", Value: decimalMoney(strconv.FormatFloat(li.Discount, 'f', -1, 64))}
	}
	if s == nil && li.Surcharge != 0 {
		s = &commercial.Adjustment{Kind: "fixed", Value: decimalMoney(strconv.FormatFloat(li.Surcharge, 'f', -1, 64))}
	}
	parts := []string{}
	if v := adjustmentText("Discount", d); v != "" {
		parts = append(parts, v)
	}
	if v := adjustmentText("Surcharge", s); v != "" {
		parts = append(parts, v)
	}
	return strings.Join(parts, "; ")
}

func AcceptFinancial(ctx context.Context, client *ent.Client, customerID int64, items []LineItem, rate string, adjustments DocumentAdjustments) (string, error) {
	in, err := FinancialInput(items, rate, adjustments)
	if err != nil {
		return "", err
	}
	if tx, ok := ctx.Value(detailsTxKey{}).(pgx.Tx); ok {
		err = tx.QueryRow(ctx, `SELECT tax_exempt FROM customers WHERE id=$1 FOR SHARE`, customerID).Scan(&in.TaxExempt)
	} else {
		var c *ent.Customer
		c, err = client.Customer.Get(ctx, customerID)
		if err == nil {
			in.TaxExempt = c.TaxExempt
		}
	}
	if err != nil {
		return "", err
	}
	raw, err := commercial.Accept(in)
	if err != nil {
		return "", commercialInputError(err)
	}
	return raw, nil
}

func commercialInputError(err error) error {
	if errors.Is(err, commercial.ErrInvalidTaxRate) {
		return errors.Join(authorization.ErrCommercialInput, ErrInvalidTaxRate, err)
	}
	if errors.Is(err, commercial.ErrInvalidLine) {
		return errors.Join(authorization.ErrCommercialInput, ErrInvalidLineItem, err)
	}
	return errors.Join(authorization.ErrCommercialInput, err)
}

func EncodeCommercialLines(items []LineItem) (string, error) {
	if items == nil {
		items = []LineItem{}
	}
	b, err := json.Marshal(items)
	return string(b), err
}

func preserveCapturedLineMoney(raw string, items []LineItem) (string, error) {
	var lines []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &lines); err != nil {
		return "", err
	}
	if len(lines) != len(items) {
		return "", authorization.ErrCommercialInput
	}
	for index, item := range items {
		for key, value := range map[string]any{"title": item.Title, "description": item.Description, "item_id": item.ItemID} {
			if _, exists := lines[index][key]; !exists && ((key == "description" && item.Description == "") || (key == "item_id" && item.ItemID == 0)) {
				continue
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				return "", err
			}
			lines[index][key] = encoded
		}
	}
	if lines == nil {
		lines = []map[string]json.RawMessage{}
	}
	encoded, err := json.Marshal(lines)
	return string(encoded), err
}

func FinancialTotals(raw, lines, rate string) (DocumentTotals, error) {
	if raw == "" {
		items, err := DecodeLineItems(lines)
		if err != nil {
			return DocumentTotals{}, err
		}
		return CalculateTotals(items, rate)
	}
	s, err := commercial.Read(raw)
	if err != nil {
		return DocumentTotals{}, err
	}
	t := DocumentTotals{Subtotal: Money{minorUnits: s.Result.PretaxCents}, Tax: Money{minorUnits: s.Result.TaxCents}, Total: Money{minorUnits: s.Result.TotalCents}}
	taxable := new(big.Int)
	for i, line := range s.Input.Lines {
		if line.Taxable {
			taxable.Add(taxable, big.NewInt(s.Result.Lines[i].AdjustedCents))
		}
	}
	if !taxable.IsInt64() {
		return DocumentTotals{}, errors.New("accepted taxable subtotal is out of range")
	}
	t.TaxableSubtotal = Money{minorUnits: taxable.Int64()}
	return t, nil
}

func FinancialAdjustments(raw string) (DocumentAdjustments, error) {
	if raw == "" {
		return DocumentAdjustments{}, nil
	}
	s, err := commercial.Read(raw)
	return DocumentAdjustments{Discount: s.Input.Discount, Surcharge: s.Input.Surcharge}, err
}

// Posting an unchanged form is not a monetary review of a legacy document.
func MonetaryChanged(oldLines, oldRate, oldFinancial string, lines *[]LineItem, rate *string, adjustments *DocumentAdjustments) bool {
	if lines == nil && rate == nil && adjustments == nil {
		return false
	}
	oldAdjustments, err := FinancialAdjustments(oldFinancial)
	if err != nil {
		return true
	}
	a, err := commercial.CapturedInput(oldLines, oldRate, oldAdjustments.Discount, oldAdjustments.Surcharge)
	if err != nil {
		return true
	}
	nextRate, nextAdjustments := oldRate, oldAdjustments
	if rate != nil {
		nextRate = *rate
	}
	if adjustments != nil {
		nextAdjustments = *adjustments
	}
	var b commercial.Input
	if lines == nil {
		b, err = commercial.CapturedInput(oldLines, nextRate, nextAdjustments.Discount, nextAdjustments.Surcharge)
	} else {
		b, err = FinancialInput(*lines, nextRate, nextAdjustments)
	}
	return err != nil || !commercial.SameInput(a, b)
}
