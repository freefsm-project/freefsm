package commercial

import (
	"encoding/json"
	"strconv"
	"strings"
)

// Decimal retains exact digits and the request's wire kind for immutable intent
// binding. Public readers construct string decimals explicitly. LegacyJSON is
// only for replay against a known pre-exact operation, never a new effect.
type Decimal struct {
	value string
	kind  byte // zero: absent legacy default; 's': string; 'n': JSON number; 'z': null
}

func NewDecimalString(value string) Decimal { return Decimal{value: value, kind: 's'} }

func (d *Decimal) UnmarshalJSON(raw []byte) error {
	text := string(raw)
	// The installed float64 decoder accepted nested null as zero. Preserve its
	// wire kind until receipt selection; new writes still reject the missing
	// positive quantity, and only version-0 comparison normalizes null to zero.
	if strings.TrimSpace(text) == "null" {
		*d = Decimal{kind: 'z'}
		return nil
	}
	kind := byte('n')
	if len(raw) > 0 && raw[0] == '"' {
		kind = 's'
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
	} else {
		// Compatibility JSON numbers retain their original decimal digits; the
		// float parse checks only the old finite transport range, never value.
		if _, err := strconv.ParseFloat(text, 64); err != nil {
			return ErrInvalid
		}
		text = capturedDecimal(text)
	}
	if !decimalPattern.MatchString(text) {
		return ErrInvalid
	}
	*d = Decimal{value: text, kind: kind}
	return nil
}
func (d Decimal) MarshalJSON() ([]byte, error) {
	if d.kind == 'z' {
		return []byte("null"), nil
	}
	if d.kind == 0 {
		return []byte("0"), nil
	}
	if d.kind == 'n' {
		return []byte(d.value), nil
	}
	return json.Marshal(d.value)
}
func (d Decimal) LegacyJSON() ([]byte, error) {
	if d.kind == 's' {
		return json.Marshal(d.value)
	}
	if d.kind == 0 || d.kind == 'z' {
		return []byte("0"), nil
	}
	value, err := strconv.ParseFloat(d.value, 64)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
func (d Decimal) String() string            { return d.value }
func (d Decimal) Float64() (float64, error) { return strconv.ParseFloat(d.value, 64) }
