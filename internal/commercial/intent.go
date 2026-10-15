package commercial

// SameInput compares monetary meaning, not cosmetic decimal spelling. This
// prevents an unchanged legacy form (1 versus 1.00) from silently repricing.
func SameInput(a, b Input) bool {
	equal := func(x, y string) bool {
		left, err := decimal(x)
		if err != nil {
			return false
		}
		right, err := decimal(y)
		return err == nil && left.Cmp(right) == 0
	}
	adjustment := func(x, y *Adjustment) bool {
		if x == nil || y == nil {
			return x == y
		}
		return x.Kind == y.Kind && equal(x.Value, y.Value)
	}
	if !equal(a.TaxRate, b.TaxRate) || a.TaxExempt != b.TaxExempt || !adjustment(a.Discount, b.Discount) || !adjustment(a.Surcharge, b.Surcharge) || len(a.Lines) != len(b.Lines) {
		return false
	}
	for i, x := range a.Lines {
		y := b.Lines[i]
		if !equal(x.Quantity, y.Quantity) || !equal(x.UnitPrice, y.UnitPrice) || x.Taxable != y.Taxable || !adjustment(x.Discount, y.Discount) || !adjustment(x.Surcharge, y.Surcharge) {
			return false
		}
	}
	return true
}

func ValidateAdjustment(a *Adjustment) error { _, err := adjusted(0, a, false); return err }
