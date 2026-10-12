package customfields

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// CheckReferences includes inactive parents and immutable recovery snapshots.
func CheckReferences(ctx context.Context, tx pgx.Tx, id int64, next *Definition, typeChanged bool) error {
	tables := []string{"estimate_invoice_conversion_cycles"}
	for _, parent := range ExistingParents() {
		tables = append(tables, parent.Table)
	}
	for _, table := range tables {
		expression := "custom_fields"
		if table == "estimate_invoice_conversion_cycles" {
			expression = "source_snapshot->'custom_fields'"
		}
		rows, err := tx.Query(ctx, `SELECT v->'value' FROM `+table+`,jsonb_array_elements(`+expression+`) v WHERE v->>'definition_id'=$1`, fmt.Sprint(id))
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			if next == nil || typeChanged {
				rows.Close()
				return &FieldError{ID: id, Name: "Custom Field", Message: "recorded values retain this definition; it cannot be deleted or change type"}
			}
			var v any
			if err = json.Unmarshal(raw, &v); err != nil {
				rows.Close()
				return err
			}
			if err = ValidateValue(*next, v); err != nil {
				rows.Close()
				return &FieldError{ID: id, Name: next.Name, Message: "the change would invalidate a recorded value"}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
