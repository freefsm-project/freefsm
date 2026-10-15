// Package commercial implements the accepted COM-03 arithmetic. Historical
// documents retain their original calculator until a reviewed monetary edit.
package commercial

import (
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"sort"
)

var ErrInvalid = errors.New("invalid commercial amounts")
var ErrInvalidTaxRate = fmt.Errorf("%w: tax rate", ErrInvalid)
var ErrInvalidLine = fmt.Errorf("%w: line", ErrInvalid)

type Adjustment struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

type Line struct {
	Quantity  string      `json:"quantity"`
	UnitPrice string      `json:"unit_price"`
	Taxable   bool        `json:"taxable"`
	Discount  *Adjustment `json:"discount,omitempty"`
	Surcharge *Adjustment `json:"surcharge,omitempty"`
}

type Input struct {
	Lines     []Line      `json:"lines"`
	TaxRate   string      `json:"tax_rate"`
	TaxExempt bool        `json:"tax_exempt"`
	Discount  *Adjustment `json:"discount,omitempty"`
	Surcharge *Adjustment `json:"surcharge,omitempty"`
}

type LineResult struct {
	BaseCents              int64 `json:"base_cents,string"`
	AfterDiscountCents     int64 `json:"after_discount_cents,string"`
	AfterSurchargeCents    int64 `json:"after_surcharge_cents,string"`
	DocumentDiscountCents  int64 `json:"document_discount_cents,string"`
	DocumentSurchargeCents int64 `json:"document_surcharge_cents,string"`
	AdjustedCents          int64 `json:"adjusted_cents,string"`
	TaxCents               int64 `json:"tax_cents,string"`
}

type Result struct {
	Lines       []LineResult `json:"lines"`
	PretaxCents int64        `json:"pretax_cents,string"`
	TaxCents    int64        `json:"tax_cents,string"`
	TotalCents  int64        `json:"total_cents,string"`
}

var decimalPattern = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)
var moneyPattern = regexp.MustCompile(`^-?[0-9]+\.[0-9]{2}$`)

func decimal(text string) (*big.Rat, error) {
	if !decimalPattern.MatchString(text) {
		return nil, ErrInvalid
	}
	v, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, ErrInvalid
	}
	return v, nil
}

// round rounds a rational number of cents halfway away from zero.
func round(v *big.Rat) (int64, error) {
	n := new(big.Int).Abs(v.Num())
	q, r := new(big.Int), new(big.Int)
	q.QuoRem(n, v.Denom(), r)
	if r.Lsh(r, 1).Cmp(v.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if v.Sign() < 0 {
		q.Neg(q)
	}
	if !q.IsInt64() {
		return 0, ErrInvalid
	}
	return q.Int64(), nil
}

func adjusted(base int64, a *Adjustment, discount bool) (int64, error) {
	if a == nil {
		return base, nil
	}
	v, err := decimal(a.Value)
	if err != nil || v.Sign() < 0 {
		return 0, ErrInvalid
	}
	switch a.Kind {
	case "fixed":
		if !moneyPattern.MatchString(a.Value) {
			return 0, ErrInvalid
		}
		v.Mul(v, big.NewRat(100, 1))
		if _, err := round(v); err != nil {
			return 0, err
		}
	case "percentage":
		v.Mul(v, big.NewRat(base, 100))
	default:
		return 0, ErrInvalid
	}
	result := new(big.Rat).SetInt64(base)
	if discount {
		result.Sub(result, v)
	} else {
		result.Add(result, v)
	}
	if discount && result.Sign() < 0 {
		return 0, nil
	}
	return round(result)
}

func Calculate(in Input) (Result, error) {
	out := Result{Lines: make([]LineResult, 0, len(in.Lines))}
	rate, err := decimal(in.TaxRate)
	if err != nil || rate.Sign() < 0 || rate.Cmp(big.NewRat(100, 1)) > 0 {
		return out, ErrInvalidTaxRate
	}
	if in.TaxExempt {
		rate.SetInt64(0)
	}
	pretax, tax, taxable := new(big.Int), new(big.Int), new(big.Int)
	for i, line := range in.Lines {
		quantity, err := decimal(line.Quantity)
		if err != nil || quantity.Sign() <= 0 {
			return Result{}, fmt.Errorf("%w %d: quantity", ErrInvalidLine, i+1)
		}
		price, err := decimal(line.UnitPrice)
		if err != nil || !moneyPattern.MatchString(line.UnitPrice) {
			return Result{}, ErrInvalidLine
		}
		if _, err := round(new(big.Rat).Mul(price, big.NewRat(100, 1))); err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrInvalidLine, err)
		}
		if price.Sign() < 0 && (line.Discount != nil || line.Surcharge != nil) {
			return Result{}, fmt.Errorf("%w %d: negative-price lines cannot carry adjustments", ErrInvalidLine, i+1)
		}
		base, err := round(new(big.Rat).Mul(new(big.Rat).Mul(quantity, price), big.NewRat(100, 1)))
		if err != nil {
			return Result{}, err
		}
		amount, err := adjusted(base, line.Discount, true)
		if err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrInvalidLine, err)
		}
		afterDiscount := amount
		amount, err = adjusted(amount, line.Surcharge, false)
		if err != nil {
			return Result{}, fmt.Errorf("%w: %v", ErrInvalidLine, err)
		}
		out.Lines = append(out.Lines, LineResult{BaseCents: base, AfterDiscountCents: afterDiscount, AfterSurchargeCents: amount, AdjustedCents: amount})
	}
	if err := allocate(out.Lines, in.Discount, true); err != nil {
		return Result{}, err
	}
	if err := allocate(out.Lines, in.Surcharge, false); err != nil {
		return Result{}, err
	}
	for i := range out.Lines {
		line := &out.Lines[i]
		if in.Lines[i].Taxable {
			taxable.Add(taxable, big.NewInt(line.AdjustedCents))
			line.TaxCents, err = round(new(big.Rat).Mul(big.NewRat(line.AdjustedCents, 100), rate))
			if err != nil {
				return Result{}, err
			}
		}
		pretax.Add(pretax, big.NewInt(line.AdjustedCents))
		tax.Add(tax, big.NewInt(line.TaxCents))
	}
	total := new(big.Int).Add(pretax, tax)
	if pretax.Sign() < 0 || total.Sign() < 0 || !pretax.IsInt64() || !tax.IsInt64() || !total.IsInt64() || !taxable.IsInt64() {
		return Result{}, ErrInvalid
	}
	out.PretaxCents, out.TaxCents, out.TotalCents = pretax.Int64(), tax.Int64(), total.Int64()
	return out, nil
}

// Each stage uses its current positive pool. Integer shares plus the largest
// remainders reconcile exactly; stable sorting preserves original line order.
func allocate(lines []LineResult, a *Adjustment, discount bool) error {
	if a == nil {
		return nil
	}
	pool := new(big.Int)
	for _, line := range lines {
		if line.AdjustedCents > 0 {
			pool.Add(pool, big.NewInt(line.AdjustedCents))
		}
	}
	if !pool.IsInt64() {
		return ErrInvalid
	}
	next, err := adjusted(pool.Int64(), a, discount)
	if err != nil {
		return err
	}
	if pool.Sign() == 0 {
		if next != 0 {
			return fmt.Errorf("%w: surcharge has no positive allocation targets", ErrInvalid)
		}
		return nil
	}
	delta := next - pool.Int64()
	if discount {
		delta = -delta
	}
	if delta == 0 {
		return nil
	}
	type share struct {
		index     int
		cents     int64
		remainder *big.Int
	}
	shares := make([]share, 0, len(lines))
	remaining := delta
	for i, line := range lines {
		if line.AdjustedCents <= 0 {
			continue
		}
		n := new(big.Int).Mul(big.NewInt(delta), big.NewInt(line.AdjustedCents))
		q, r := new(big.Int), new(big.Int)
		q.QuoRem(n, pool, r)
		shares = append(shares, share{i, q.Int64(), r})
		remaining -= q.Int64()
	}
	sort.SliceStable(shares, func(i, j int) bool { return shares[i].remainder.Cmp(shares[j].remainder) > 0 })
	for i := range shares {
		s := shares[i]
		if int64(i) < remaining {
			s.cents++
		}
		if discount {
			lines[s.index].AdjustedCents -= s.cents
			lines[s.index].DocumentDiscountCents = s.cents
		} else {
			lines[s.index].AdjustedCents += s.cents
			lines[s.index].DocumentSurchargeCents = s.cents
		}
	}
	return nil
}
