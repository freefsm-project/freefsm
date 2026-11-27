// Package liststate validates URL presentation state without conferring authority.
package liststate

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"
)

const MaxBytes = 16 * 1024

var ErrInvalid = errors.New("invalid list state")

type Spec struct {
	Fields   map[string]func([]string) error
	Defaults url.Values
}

type Query struct {
	Values   url.Values
	Explicit bool
	Reset    bool
}

// Parse treats any supplied state as a complete override, filling only route
// defaults. Saved state must be parsed separately using this same specification.
// Validators own multiplicity: use Singleton for ordinary fields and Set only
// for explicitly repeatable fields. Errors deliberately omit submitted values.
func Parse(values url.Values, spec Spec) (Query, error) {
	if err := Bounded(values); err != nil {
		return Query{}, err
	}
	// A post-save notification is transport, not a query-state key. It must
	// neither replace remembered filters nor enter durable/canonical state.
	// Validate before removal so malformed transport is not silently ignored.
	values = clone(values)
	if flash, ok := values["flash"]; ok {
		if err := Flash(flash); err != nil {
			return Query{}, err
		}
		delete(values, "flash")
	}
	if reset, ok := values["reset"]; ok {
		if len(values) != 1 || len(reset) != 1 || reset[0] != "1" {
			return Query{}, fmt.Errorf("%w: reset must stand alone", ErrInvalid)
		}
		return Query{Values: url.Values{}, Reset: true}, nil
	}
	result := clone(spec.Defaults)
	for key, value := range values {
		result[key] = slices.Clone(value)
	}
	for key, value := range result {
		validate, ok := spec.Fields[key]
		if !ok || validate == nil {
			return Query{}, fmt.Errorf("%w: unknown field", ErrInvalid)
		}
		if len(value) == 0 {
			return Query{}, fmt.Errorf("%w: missing value", ErrInvalid)
		}
		if err := validate(value); err != nil {
			return Query{}, fmt.Errorf("%w: field %s", ErrInvalid, key)
		}
		slices.Sort(value)
	}
	if err := Bounded(result); err != nil {
		return Query{}, err
	}
	return Query{Values: result, Explicit: len(values) != 0}, nil
}

// Bounded checks the serialized URL size and UTF-8 before further processing.
func Bounded(values url.Values) error {
	// Bound before Encode as well, so pathological inputs cannot force a large
	// allocation before the encoded-size check.
	size := 0
	for key, values := range values {
		if !utf8.ValidString(key) || len(values) == 0 {
			return ErrInvalid
		}
		for _, value := range values {
			size += len(key) + len(value) + 2
			if size > MaxBytes || !utf8.ValidString(value) {
				return ErrInvalid
			}
		}
	}
	if len(values.Encode()) > MaxBytes {
		return ErrInvalid
	}
	return nil
}

// Canonical produces stable query text, including q= to make an empty query
// explicit. Repeated set values sort without changing the caller's slices.
func Canonical(values url.Values) string {
	copy := clone(values)
	delete(copy, "flash")
	if _, ok := copy["q"]; !ok {
		copy.Set("q", "")
	}
	for _, values := range copy {
		slices.Sort(values)
	}
	return copy.Encode()
}

// Persistent returns an independent copy without transient navigation or form
// transport state. Callers pass validated route values, never result content.
func Persistent(values url.Values) url.Values {
	copy := clone(values)
	for _, key := range []string{"page", "reset", "flash", "scroll", "scroll_x", "scroll_y", "csrf_token", "operation_id", "save_action_id", "save_revision", "save_history", "_"} {
		delete(copy, key)
	}
	return copy
}

// Flash accepts a short single-line notification. HTML escaping remains the
// rendering layer's responsibility; validation does not mark text as trusted.
func Flash(values []string) error {
	if err := Text(500)(values); err != nil {
		return err
	}
	for _, char := range values[0] {
		if unicode.IsControl(char) {
			return ErrInvalid
		}
	}
	return nil
}

func clone(values url.Values) url.Values {
	copy := make(url.Values, len(values))
	for key, value := range values {
		copy[key] = slices.Clone(value)
	}
	return copy
}

func Singleton(validate func(string) error) func([]string) error {
	return func(values []string) error {
		if len(values) != 1 {
			return ErrInvalid
		}
		return validate(values[0])
	}
}

func Text(maxCharacters int) func([]string) error {
	return Singleton(func(value string) error {
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maxCharacters {
			return ErrInvalid
		}
		return nil
	})
}

func Enum(allowed ...string) func([]string) error {
	return Singleton(func(value string) error {
		if !slices.Contains(allowed, value) {
			return ErrInvalid
		}
		return nil
	})
}

// Integer accepts decimal digits only (no signs, whitespace or exponent).
func Integer(min, max int64) func([]string) error {
	return Singleton(func(value string) error {
		if value == "" {
			return ErrInvalid
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return ErrInvalid
			}
		}
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number < min || number > max {
			return ErrInvalid
		}
		return nil
	})
}

// Optional permits an explicit empty singleton, preserving clear-filter URLs.
func Optional(validate func([]string) error) func([]string) error {
	return func(values []string) error {
		if len(values) == 1 && values[0] == "" {
			return nil
		}
		return validate(values)
	}
}

func Date(values []string) error {
	return Singleton(func(value string) error {
		date, err := time.Parse("2006-01-02", value)
		if err != nil || date.Format("2006-01-02") != value {
			return ErrInvalid
		}
		return nil
	})(values)
}

// Set validates an explicitly repeatable enum, rejecting duplicate selections.
func Set(allowed ...string) func([]string) error {
	return func(values []string) error {
		if len(values) == 0 {
			return ErrInvalid
		}
		seen := make(map[string]bool, len(values))
		for _, value := range values {
			if seen[value] || !slices.Contains(allowed, value) {
				return ErrInvalid
			}
			seen[value] = true
		}
		return nil
	}
}

// CommonFields returns a fresh validator map for route-specific extension.
// Each route supplies its own sort choices; type is intentionally absent.
func CommonFields(sorts ...string) map[string]func([]string) error {
	return map[string]func([]string) error{
		"q": Text(500), "sort": Enum(sorts...), "page": Integer(1, 1_000_000),
		"per_page": Enum("25", "50", "100"), "state": Enum("active", "archived", "all"),
	}
}
