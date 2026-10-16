// Package customfields owns the shared typed-value contract. Parent domains
// choose the save mode; lifecycle actions do not call details validation.
package customfields

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

type FieldError struct {
	ID            int64
	Name, Message string
	Reason        string
}

func (e *FieldError) Error() string { return e.Name + ": " + e.Message }

type Value struct {
	DefinitionID int64 `json:"definition_id"`
	Value        any   `json:"value"`
}
type Definition struct {
	ID             int64
	Name, Type     string
	Required       bool
	Retired        bool
	Precision      *int
	Default        *string
	Options        string
	RetiredOptions string
}

type SaveMode int

const (
	Details SaveMode = iota
	Creation
	ChangedValues
)

// Validate reads definitions on the parent's transaction. Definition writers
// share the authorization transaction mutex, including insertion of new fields.
func Validate(ctx context.Context, tx pgx.Tx, typ string, old string, input *string, mode SaveMode, company int64) (string, error) {
	creating := mode == Creation
	rows, err := tx.Query(ctx, `SELECT id,name,field_type,required,precision,default_value,options::text,retired,retired_options::text FROM custom_field_definitions WHERE object_type=$1 AND ($2::bigint=0 OR company_id=$2 OR company_id IS NULL) ORDER BY id`, typ, company)
	if err != nil {
		return "", err
	}
	defs := map[int64]Definition{}
	for rows.Next() {
		var d Definition
		if err = rows.Scan(&d.ID, &d.Name, &d.Type, &d.Required, &d.Precision, &d.Default, &d.Options, &d.Retired, &d.RetiredOptions); err != nil {
			rows.Close()
			return "", err
		}
		defs[d.ID] = d
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return "", err
	}
	values := map[int64]any{}
	var previous []Value
	if old != "" {
		if err = json.Unmarshal([]byte(old), &previous); err != nil {
			return "", err
		}
	}
	for _, v := range previous {
		values[v.DefinitionID] = v.Value
	}
	if creating {
		for _, d := range defs {
			if d.Default != nil && !d.Retired {
				v, err := DefaultValue(d)
				if err != nil {
					return "", err
				}
				values[d.ID] = v
				if err := ValidateSelection(d, nil, v); err != nil {
					return "", err
				}
			}
		}
	}
	seen := map[int64]bool{}
	if input != nil {
		var entered []Value
		decoder := json.NewDecoder(strings.NewReader(*input))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&entered); err != nil || entered == nil {
			return "", &FieldError{Name: "Custom Fields", Message: "values must be an array of field identities and typed values", Reason: "invalid_field_shape"}
		}
		for _, v := range entered {
			d, ok := defs[v.DefinitionID]
			if !ok || seen[v.DefinitionID] {
				return "", &FieldError{Name: "Custom Fields", Message: "unknown or repeated field", Reason: "invalid_field_identity"}
			}
			seen[v.DefinitionID] = true
			if d.Retired {
				return "", &FieldError{ID: d.ID, Name: d.Name, Message: "retired fields cannot be edited", Reason: "retired_field"}
			}
			if err := ValidateValue(d, v.Value); err != nil {
				return "", err
			}
			if err := ValidateSelection(d, values[d.ID], v.Value); err != nil {
				return "", err
			}
			values[v.DefinitionID] = v.Value
		}
	}
	for _, d := range defs {
		if d.Required && !d.Retired && Empty(values[d.ID]) && !(creating && (typ == "job" || typ == "lease")) && (mode != ChangedValues || seen[d.ID]) {
			return "", &FieldError{ID: d.ID, Name: d.Name, Message: "a value is required", Reason: "required_field"}
		}
	}
	result := make([]Value, 0, len(values))
	for id, v := range values {
		result = append(result, Value{id, v})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].DefinitionID < result[j].DefinitionID })
	b, err := json.Marshal(result)
	return string(b), err
}

var decimal = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]+)?$`)

// Retired option identities stay reserved. Existing selections can be retained
// or removed, but can never be newly selected, including through a default.
func ValidateSelection(d Definition, previous, next any) error {
	if d.RetiredOptions == "" || d.RetiredOptions == "[]" {
		return nil
	}
	retired, err := Options(d.RetiredOptions)
	if err != nil {
		return err
	}
	selections := func(v any) map[string]bool {
		result := map[string]bool{}
		switch v := v.(type) {
		case string:
			result[v] = true
		case []string:
			for _, s := range v {
				result[s] = true
			}
		case []any:
			for _, s := range v {
				if s, ok := s.(string); ok {
					result[s] = true
				}
			}
		}
		return result
	}
	old := selections(previous)
	for selected := range selections(next) {
		if retired[selected] && !old[selected] {
			return &FieldError{ID: d.ID, Name: d.Name, Message: "retired options cannot be newly selected", Reason: "retired_option"}
		}
	}
	return nil
}

func ValidateValue(d Definition, value any) error {
	v, scalar := value.(string)
	if d.Type != "multiselect" && !scalar {
		return &FieldError{ID: d.ID, Name: d.Name, Message: "a single text value is required", Reason: "invalid_field_type"}
	}
	if d.Type == "multiselect" {
		switch value.(type) {
		case []string, []any:
		default:
			return &FieldError{ID: d.ID, Name: d.Name, Message: "choose a list of options", Reason: "invalid_field_type"}
		}
	}
	if Empty(value) {
		return nil
	}
	message := ""
	switch d.Type {
	case "text":
		if strings.ContainsAny(v, "\r\n") {
			message = "use a single line"
		}
	case "textarea":
	case "date":
		if date, err := time.Parse("2006-01-02", v); err != nil || len(v) != 10 || date.Year() < 1 {
			message = "enter a valid local date (YYYY-MM-DD)"
		}
	case "checkbox":
		if v != "true" && v != "false" {
			message = "choose Yes or No"
		}
	case "number":
		if !decimal.MatchString(v) {
			message = "enter a decimal number"
		} else if d.Precision == nil {
			message = "number precision requires definition review"
		} else if dot := strings.IndexByte(v, '.'); dot >= 0 && len(v)-dot-1 > *d.Precision {
			message = "number exceeds the configured decimal precision"
		}
	case "select", "multiselect":
		options, err := Options(d.Options)
		if err != nil {
			return err
		}
		selected := []string{v}
		if d.Type == "multiselect" {
			b, _ := json.Marshal(value)
			if scalar || json.Unmarshal(b, &selected) != nil {
				message = "choose a list of options"
			}
		}
		seen := map[string]bool{}
		for _, id := range selected {
			if !options[id] || seen[id] {
				message = "choose valid, distinct options from this field"
			}
			seen[id] = true
		}
	default:
		message = "unsupported field type requires review"
	}
	if message != "" {
		reason := "invalid_" + d.Type
		if d.Type == "select" || d.Type == "multiselect" {
			reason = "invalid_option"
		}
		return &FieldError{ID: d.ID, Name: d.Name, Message: message, Reason: reason}
	}
	return nil
}

func Empty(v any) bool {
	if v == nil {
		return true
	}
	switch v := v.(type) {
	case string:
		return v == ""
	case []string:
		return len(v) == 0
	case []any:
		return len(v) == 0
	}
	return false
}
func DefaultValue(d Definition) (any, error) {
	if d.Default == nil {
		return nil, nil
	}
	if d.Type != "multiselect" {
		return *d.Default, nil
	}
	var v []string
	if err := json.Unmarshal([]byte(*d.Default), &v); err != nil || v == nil {
		return nil, &FieldError{ID: d.ID, Name: d.Name, Message: "multiselect default must be a JSON array", Reason: "invalid_field_type"}
	}
	return v, nil
}

// Option text is the stable legacy option identity. It is never normalized or
// translated in recorded values. Normalization is used only to detect collisions.
func Options(raw string) (map[string]bool, error) {
	var opts []string
	if json.Unmarshal([]byte(raw), &opts) != nil || opts == nil {
		return nil, &FieldError{Name: "Options", Message: "must be a JSON array of strings"}
	}
	result := map[string]bool{}
	names := map[string]bool{}
	for _, o := range opts {
		name := nameKey(o)
		if name == "" || names[name] {
			return nil, &FieldError{Name: "Options", Message: fmt.Sprintf("blank or duplicate option %q", o)}
		}
		names[name] = true
		result[o] = true
	}
	return result, nil
}

// nameKey matches Unicode simple case folding (including e.g. both Greek
// sigmas), without altering the original persisted name or option identity.
func nameKey(raw string) string {
	return strings.Map(func(r rune) rune {
		minimum := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < minimum {
				minimum = next
			}
		}
		return minimum
	}, strings.TrimSpace(raw))
}
