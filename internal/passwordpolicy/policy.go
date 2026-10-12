// Package passwordpolicy is the shared password-setting boundary. Length and
// character classes deliberately match JavaScript string.length and ASCII regexes.
package passwordpolicy

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"unicode/utf16"
)

type Policy struct {
	MinLength                    int
	Upper, Lower, Digit, Special bool
}

func Default() Policy { return Policy{8, true, true, true, true} }

func (p Policy) Validate(password string) error {
	if p.MinLength > 72 {
		return fmt.Errorf("password policy needs Administrator review: choose a minimum between 6 and 72 in Settings / Advanced security")
	}
	if len(password) > 72 {
		return fmt.Errorf("password must be no more than 72 UTF-8 bytes")
	}
	if len(utf16.Encode([]rune(password))) < p.MinLength {
		return fmt.Errorf("password must be at least %d characters", p.MinLength)
	}
	var upper, lower, digit, special bool
	for _, r := range password {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			special = true
		}
	}
	for _, rule := range []struct {
		required, met bool
		name          string
	}{{p.Upper, upper, "an uppercase letter"}, {p.Lower, lower, "a lowercase letter"}, {p.Digit, digit, "a digit"}, {p.Special, special, "a special character"}} {
		if rule.required && !rule.met {
			return fmt.Errorf("password must contain %s", rule.name)
		}
	}
	return nil
}

// Load locks the policy until commit, including against settings writers that
// don't use the shared authorization mutex.
func Load(ctx context.Context, tx pgx.Tx, companyID int64) (Policy, error) {
	var p Policy
	err := tx.QueryRow(ctx, `SELECT password_min_length,password_require_uppercase,password_require_lowercase,password_require_digit,password_require_special FROM company_settings WHERE company_id=$1 FOR SHARE`, companyID).Scan(&p.MinLength, &p.Upper, &p.Lower, &p.Digit, &p.Special)
	return p, err
}
