package customfields

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
)

type CapturedDefinition struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"field_type"`
	Precision *int   `json:"precision"`
	Options   string `json:"options"`
}
type CycleEvidence struct {
	Pairs       map[int64]int64      `json:"field_pairs"`
	Definitions []CapturedDefinition `json:"field_definitions"`
}

// CheckCycleValues preserves the representability promised by an active cycle,
// independently of the definitions that will govern the next conversion.
func CheckCycleValues(ctx context.Context, tx pgx.Tx, typ string, id int64, encoded string) error {
	if typ != "invoice" || id == 0 {
		return nil
	}
	var snapshot []byte
	err := tx.QueryRow(ctx, `SELECT conversion_field_snapshot(id) FROM estimate_invoice_conversion_cycles WHERE invoice_id=$1 AND reverted_at IS NULL`, id).Scan(&snapshot)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var capture CycleEvidence
	if err = json.Unmarshal(snapshot, &capture); err != nil {
		return err
	}
	if capture.Pairs == nil {
		return &FieldError{Name: "Conversion", Message: "historical pairing requires explicit review before editing values"}
	}
	var values []Value
	if err = json.Unmarshal([]byte(encoded), &values); err != nil {
		return err
	}
	for _, v := range values {
		for source, target := range capture.Pairs {
			if target != v.DefinitionID {
				continue
			}
			found := false
			for _, d := range capture.Definitions {
				if d.ID != source {
					continue
				}
				found = true
				if err = ValidateValue(Definition{ID: v.DefinitionID, Name: d.Name, Type: d.Type, Precision: d.Precision, Options: d.Options}, v.Value); err != nil {
					return err
				}
			}
			if !found {
				return fmt.Errorf("captured definition %d requires review", source)
			}
			var current Definition
			current.ID = source
			if err = tx.QueryRow(ctx, `SELECT name,field_type,precision,options::text FROM custom_field_definitions WHERE id=$1`, source).Scan(&current.Name, &current.Type, &current.Precision, &current.Options); err != nil {
				return err
			}
			if err = ValidateValue(current, v.Value); err != nil {
				return err
			}
		}
	}
	return nil
}
