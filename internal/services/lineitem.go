package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/freefsm-project/freefsm/internal/ent"
)

var (
	ErrMalformedLineItems = errors.New("malformed line items JSON")
	ErrInvalidLineItem    = errors.New("invalid line item")
	ErrInvalidTaxRate     = errors.New("invalid tax rate")
)

// Money is an amount rounded to currency minor units (cents).
type Money struct {
	minorUnits int64
}

func (m Money) MinorUnits() int64 { return m.minorUnits }

func MoneyFromMajorUnits(value float64) (Money, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Money{}, fmt.Errorf("money amount must be finite")
	}
	return roundMoney(decimalFromFloat(value))
}

func (m Money) MajorUnits() float64 { return float64(m.minorUnits) / 100 }

func (m Money) Format() string {
	return fmt.Sprintf("%.2f", m.MajorUnits())
}

func (m Money) Add(other Money) (Money, error) {
	result := m
	if err := addMoney(&result, other); err != nil {
		return Money{}, err
	}
	return result, nil
}

func (m Money) Sub(other Money) (Money, error) {
	if other.minorUnits == math.MinInt64 {
		return Money{}, fmt.Errorf("money total is out of range")
	}
	return m.Add(Money{minorUnits: -other.minorUnits})
}

func (m Money) Compare(other Money) int {
	switch {
	case m.minorUnits < other.minorUnits:
		return -1
	case m.minorUnits > other.minorUnits:
		return 1
	default:
		return 0
	}
}

type DocumentTotals struct {
	Subtotal        Money
	TaxableSubtotal Money
	Tax             Money
	Total           Money
}

type LineItem struct {
	ItemID      int64   `json:"item_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	UnitPrice   float64 `json:"unit_price"`
	Quantity    float64 `json:"quantity"`
	Taxable     bool    `json:"taxable"`
	TaxRate     string  `json:"tax_rate"`
	Discount    float64 `json:"discount"`
	Surcharge   float64 `json:"surcharge"`
}

// CatalogSnapshot contains only the catalog values copied into a document line.
// Once created, a LineItem is independent of later catalog changes.
type CatalogSnapshot struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	UnitPrice   float64 `json:"unit_price"`
	Taxable     bool    `json:"taxable"`
	TaxRate     string  `json:"tax_rate"`
}

// CustomerTaxContext supplies already-decided tax context for editor bootstrap.
// The line-item module serializes it but does not decide customer tax policy.
type CustomerTaxContext interface {
	DefaultTaxRate() string
	CustomerTaxExemptions() map[int64]bool
}

type EditorBootstrap struct {
	ItemsJSON         string
	ExistingItemsJSON string
	CustomersJSON     string
}

func CatalogSnapshotFromItem(item *ent.Item) CatalogSnapshot {
	if item == nil {
		return CatalogSnapshot{}
	}
	return CatalogSnapshot{
		ID:          item.ID,
		Name:        item.Name,
		Description: item.Description,
		UnitPrice:   item.UnitPrice,
		Taxable:     item.Taxable,
		TaxRate:     item.TaxRate,
	}
}

func (snapshot CatalogSnapshot) NewLineItem(quantity float64) (LineItem, error) {
	line := LineItem{
		ItemID:      snapshot.ID,
		Title:       snapshot.Name,
		Description: snapshot.Description,
		UnitPrice:   snapshot.UnitPrice,
		Quantity:    quantity,
		Taxable:     snapshot.Taxable,
		TaxRate:     snapshot.TaxRate,
	}
	if err := ValidateLineItems([]LineItem{line}); err != nil {
		return LineItem{}, fmt.Errorf("create line item from catalog snapshot: %w", err)
	}
	return line, nil
}

func ReferencesItem(items []LineItem, itemID int64) bool {
	if itemID <= 0 {
		return false
	}
	for _, item := range items {
		if item.ItemID == itemID {
			return true
		}
	}
	return false
}

func EncodeEditorBootstrap(catalog []CatalogSnapshot, existingLineItems string, taxContext CustomerTaxContext) (EditorBootstrap, error) {
	if catalog == nil {
		catalog = []CatalogSnapshot{}
	}
	itemsJSON, err := json.Marshal(catalog)
	if err != nil {
		return EditorBootstrap{}, fmt.Errorf("encode catalog snapshots: %w", err)
	}

	existing, err := DecodeLineItems(existingLineItems)
	if err != nil {
		return EditorBootstrap{}, fmt.Errorf("decode existing line items: %w", err)
	}
	existingJSON, err := EncodeLineItems(existing)
	if err != nil {
		return EditorBootstrap{}, fmt.Errorf("encode existing line items: %w", err)
	}

	if taxContext == nil {
		return EditorBootstrap{}, errors.New("customer tax context is required")
	}
	type customerTaxInfo struct {
		TaxExempt bool `json:"tax_exempt"`
	}
	customers := make(map[int64]customerTaxInfo, len(taxContext.CustomerTaxExemptions()))
	for id, exempt := range taxContext.CustomerTaxExemptions() {
		customers[id] = customerTaxInfo{TaxExempt: exempt}
	}
	customersJSON, err := json.Marshal(struct {
		DefaultTaxRate string                    `json:"default_tax_rate"`
		Customers      map[int64]customerTaxInfo `json:"customers"`
	}{
		DefaultTaxRate: taxContext.DefaultTaxRate(),
		Customers:      customers,
	})
	if err != nil {
		return EditorBootstrap{}, fmt.Errorf("encode customer tax context: %w", err)
	}

	return EditorBootstrap{
		ItemsJSON:         string(itemsJSON),
		ExistingItemsJSON: existingJSON,
		CustomersJSON:     string(customersJSON),
	}, nil
}

func DecodeLineItems(s string) ([]LineItem, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var items []LineItem
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedLineItems, err)
	}
	return items, nil
}

func ValidateLineItems(items []LineItem) error {
	for i, item := range items {
		if strings.TrimSpace(item.Title) == "" {
			return fmt.Errorf("%w %d: title is required", ErrInvalidLineItem, i)
		}
		if item.Quantity <= 0 || math.IsNaN(item.Quantity) || math.IsInf(item.Quantity, 0) {
			return fmt.Errorf("%w %d: quantity must be positive and finite", ErrInvalidLineItem, i)
		}
		for name, value := range map[string]float64{
			"unit price": item.UnitPrice,
			"discount":   item.Discount,
			"surcharge":  item.Surcharge,
		} {
			if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("%w %d: %s must be nonnegative and finite", ErrInvalidLineItem, i, name)
			}
		}
	}
	return nil
}

func LineAmount(item LineItem) (Money, error) {
	if err := ValidateLineItems([]LineItem{item}); err != nil {
		return Money{}, err
	}
	unitPrice := decimalFromFloat(item.UnitPrice)
	quantity := decimalFromFloat(item.Quantity)
	discount := decimalFromFloat(item.Discount)
	surcharge := decimalFromFloat(item.Surcharge)

	amount := new(big.Rat).Mul(unitPrice, quantity)
	amount.Sub(amount, discount)
	amount.Add(amount, surcharge)
	return roundMoney(amount)
}

func CalculateTotals(items []LineItem, taxRate string) (DocumentTotals, error) {
	if err := ValidateLineItems(items); err != nil {
		return DocumentTotals{}, err
	}
	rate, err := parsePercentage(taxRate)
	if err != nil {
		return DocumentTotals{}, err
	}

	var totals DocumentTotals
	for _, item := range items {
		amount, err := LineAmount(item)
		if err != nil {
			return DocumentTotals{}, err
		}
		if err := addMoney(&totals.Subtotal, amount); err != nil {
			return DocumentTotals{}, err
		}
		if item.Taxable {
			if err := addMoney(&totals.TaxableSubtotal, amount); err != nil {
				return DocumentTotals{}, err
			}
		}
	}

	taxable := new(big.Rat).SetInt64(totals.TaxableSubtotal.minorUnits)
	taxable.Quo(taxable, big.NewRat(100, 1)) // convert cents to major units
	taxable.Mul(taxable, rate)
	taxable.Quo(taxable, big.NewRat(100, 1)) // percentage
	totals.Tax, err = roundMoney(taxable)
	if err != nil {
		return DocumentTotals{}, err
	}
	totals.Total = totals.Subtotal
	if err := addMoney(&totals.Total, totals.Tax); err != nil {
		return DocumentTotals{}, err
	}
	return totals, nil
}

func EncodeLineItems(items []LineItem) (string, error) {
	if err := ValidateLineItems(items); err != nil {
		return "", fmt.Errorf("validate line items: %w", err)
	}
	if items == nil {
		items = []LineItem{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", fmt.Errorf("encode line items: %w", err)
	}
	return string(b), nil
}

func decimalFromFloat(value float64) *big.Rat {
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(value, 'f', -1, 64))
	if !ok {
		panic("validated finite float could not be converted to decimal")
	}
	return r
}

func parsePercentage(value string) (*big.Rat, error) {
	value = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), "%"))
	rate, ok := new(big.Rat).SetString(value)
	if !ok || rate.Sign() < 0 || rate.Cmp(big.NewRat(100, 1)) > 0 {
		return nil, fmt.Errorf("%w: must be a percentage from 0 through 100", ErrInvalidTaxRate)
	}
	return rate, nil
}

func roundMoney(amount *big.Rat) (Money, error) {
	scaled := new(big.Rat).Mul(amount, big.NewRat(100, 1))
	numerator := new(big.Int).Set(scaled.Num())
	denominator := scaled.Denom()
	sign := numerator.Sign()
	numerator.Abs(numerator)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if sign < 0 {
		quotient.Neg(quotient)
	}
	if !quotient.IsInt64() {
		return Money{}, fmt.Errorf("money amount is out of range")
	}
	return Money{minorUnits: quotient.Int64()}, nil
}

func addMoney(total *Money, amount Money) error {
	result := new(big.Int).Add(big.NewInt(total.minorUnits), big.NewInt(amount.minorUnits))
	if !result.IsInt64() {
		return fmt.Errorf("money total is out of range")
	}
	total.minorUnits = result.Int64()
	return nil
}
