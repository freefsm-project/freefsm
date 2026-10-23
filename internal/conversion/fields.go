package conversion

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/jackc/pgx/v5"
)

// invoiceFieldInput is the common preview/commit merge. Only actual source
// values override explicit Invoice input. An absent paired source suppresses
// the Invoice default, so missing required values must be deliberately supplied.
func invoiceFieldInput(ctx context.Context, tx pgx.Tx, company int64, source []byte, input *string) (string, map[int64]int64, error) {
	encoded, err := mapCustomFields(ctx, tx, company, source, "estimate", "invoice", false)
	if err != nil {
		return "", nil, err
	}
	var paired, entered []customfields.Value
	if err = json.Unmarshal(encoded, &paired); err != nil {
		return "", nil, err
	}
	if input != nil {
		decoder := json.NewDecoder(strings.NewReader(*input))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&entered); err != nil || entered == nil {
			return "", nil, &customfields.FieldError{Name: "Custom Fields", Message: "values must be an array", Reason: "invalid_field_shape"}
		}
		if err = decoder.Decode(new(any)); err != io.EOF {
			return "", nil, &customfields.FieldError{Name: "Custom Fields", Message: "unexpected trailing input", Reason: "invalid_field_shape"}
		}
		seen := map[int64]bool{}
		for _, v := range entered {
			if seen[v.DefinitionID] {
				return "", nil, &customfields.FieldError{Name: "Custom Fields", Message: "repeated field", Reason: "invalid_field_identity"}
			}
			seen[v.DefinitionID] = true
		}
	}
	pairedIDs := map[int64]bool{}
	for _, v := range paired {
		pairedIDs[v.DefinitionID] = true
	}
	for _, v := range entered {
		if !pairedIDs[v.DefinitionID] {
			paired = append(paired, v)
		}
	}
	pairs, err := customFieldPairs(ctx, tx, company)
	if err != nil {
		return "", nil, err
	}
	present := map[int64]bool{}
	for _, v := range paired {
		present[v.DefinitionID] = true
	}
	for _, id := range pairs {
		if present[id] {
			continue
		}
		var typ string
		if err = tx.QueryRow(ctx, `SELECT field_type FROM custom_field_definitions WHERE id=$1`, id).Scan(&typ); err != nil {
			return "", nil, err
		}
		var empty any = ""
		if typ == "multiselect" {
			empty = []string{}
		}
		paired = append(paired, customfields.Value{DefinitionID: id, Value: empty})
		present[id] = true
	}
	// Materialize prospective defaults in the shared input as well as in final
	// validation, so an incomplete preview still shows the values that will copy.
	rows, err := tx.Query(ctx, `SELECT id,name,field_type,default_value,options::text FROM custom_field_definitions WHERE custom_field_applies_to_company(company_id,$1) AND object_type='invoice' AND NOT retired AND default_value IS NOT NULL ORDER BY id`, company)
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var d customfields.Definition
		if err = rows.Scan(&d.ID, &d.Name, &d.Type, &d.Default, &d.Options); err != nil {
			return "", nil, err
		}
		if present[d.ID] {
			continue
		}
		value, err := customfields.DefaultValue(d)
		if err != nil {
			return "", nil, err
		}
		paired = append(paired, customfields.Value{DefinitionID: d.ID, Value: value})
	}
	if err = rows.Err(); err != nil {
		return "", nil, err
	}
	encoded, err = json.Marshal(paired)
	return string(encoded), pairs, err
}
