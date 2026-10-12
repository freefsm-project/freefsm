package customfields

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// CheckReferences includes inactive parents and immutable recovery snapshots.
func CheckReferences(ctx context.Context, tx pgx.Tx, id int64, next *Definition, typeChanged bool) error {
	// Pairing references are dependencies even when neither record has a value.
	var captured bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM estimate_invoice_conversion_cycles c,jsonb_each_text(coalesce(conversion_field_snapshot(c.id)->'field_pairs','{}')) p WHERE p.key=$1 OR p.value=$1)`, fmt.Sprint(id)).Scan(&captured); err != nil {
		return err
	}
	if captured && (next == nil || typeChanged) {
		return &FieldError{ID: id, Name: "Custom Field", Message: "captured conversion pairing retains this definition and its type"}
	}
	if next != nil {
		// A definition on the hidden Estimate must still represent the active
		// Invoice value when the original cycle is recovered.
		rows, err := tx.Query(ctx, `SELECT v->'value' FROM estimate_invoice_conversion_cycles c JOIN invoices i ON i.id=c.invoice_id,jsonb_array_elements(i.custom_fields) v WHERE c.reverted_at IS NULL AND conversion_field_snapshot(c.id)->'field_pairs'->>$1=v->>'definition_id'`, fmt.Sprint(id))
		if err != nil {
			return err
		}
		for rows.Next() {
			var raw []byte
			if err = rows.Scan(&raw); err != nil {
				rows.Close()
				return err
			}
			var value any
			if err = json.Unmarshal(raw, &value); err == nil {
				err = ValidateValue(*next, value)
			}
			if err != nil {
				rows.Close()
				return &FieldError{ID: id, Name: next.Name, Message: "change would make active conversion recovery unrepresentable"}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
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
