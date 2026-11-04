// Package invoice owns the effective Invoice slot precedence from ADR 0003.
package invoice

func EffectiveCategory(manual, settlement string, active bool) string {
	switch {
	case manual == "invoice:void":
		return manual
	case manual == "invoice:draft" && !active:
		return manual
	case settlement == "partially_paid" && active:
		return "invoice:partially_paid"
	case settlement == "paid" && (active || manual != "invoice:draft"):
		return "invoice:paid"
	default:
		return manual
	}
}
