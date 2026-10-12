package authorization

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/freefsm-project/freefsm/internal/customfields"
	"github.com/jackc/pgx/v5"
)

type FieldInput struct {
	ObjectType, Name, Type, Options, ConversionKey string
	Required                                       bool
	SortOrder                                      int
	Precision                                      *int
	Default                                        *string
}

// SaveField serializes definition changes with details saves and authority
// changes, and publishes the definition and protected audit in one commit.
func (s *Service) SaveField(ctx context.Context, a Actor, id int64, in FieldInput) (int64, error) {
	err := s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("configuration.fields.manage", Configuration) {
			return ErrForbidden
		}
		var before json.RawMessage
		if id != 0 {
			if err := tx.QueryRow(ctx, `SELECT object_type,to_jsonb(d) FROM custom_field_definitions d WHERE id=$1 AND (company_id=$2 OR company_id IS NULL) FOR UPDATE`, id, p.CompanyID).Scan(&in.ObjectType, &before); err == pgx.ErrNoRows {
				return ErrNotFound
			} else if err != nil {
				return err
			}
		}
		in.Name = strings.TrimSpace(in.Name)
		in.ConversionKey = strings.TrimSpace(in.ConversionKey)
		if in.ConversionKey != "" {
			valid, _ := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9_-]*$`, in.ConversionKey)
			if !valid || (in.ObjectType != "estimate" && in.ObjectType != "invoice") {
				return &customfields.FieldError{Name: "Conversion key", Message: "use letters, numbers, underscores or hyphens on an Estimate or Invoice field"}
			}
		}
		if in.Name == "" {
			return &customfields.FieldError{Name: "Name", Message: "is required"}
		}
		if !customfields.SupportsParent(in.ObjectType) {
			return &customfields.FieldError{Name: "Parent", Message: "unsupported record type"}
		}
		if !customfields.SupportsType(in.Type) {
			return &customfields.FieldError{Name: "Type", Message: "unsupported field type"}
		}
		if in.Type == "number" && (in.Precision == nil || *in.Precision < 0 || *in.Precision > 18) {
			return &customfields.FieldError{Name: in.Name, Message: "choose decimal precision from 0 to 18"}
		}
		if in.Type != "number" {
			in.Precision = nil
		}
		if in.Options == "" {
			in.Options = "[]"
		}
		if id != 0 {
			var oldType string
			if err := tx.QueryRow(ctx, `SELECT field_type FROM custom_field_definitions WHERE id=$1`, id).Scan(&oldType); err != nil {
				return err
			}
			next := customfields.Definition{ID: id, Name: in.Name, Type: in.Type, Precision: in.Precision, Options: in.Options}
			if err := customfields.CheckReferences(ctx, tx, id, &next, oldType != in.Type); err != nil {
				return err
			}
		}
		if _, err := customfields.Options(in.Options); err != nil {
			return err
		}
		if in.Default != nil {
			d := customfields.Definition{Name: in.Name, Type: in.Type, Precision: in.Precision, Options: in.Options, Default: in.Default}
			v, err := customfields.DefaultValue(d)
			if err != nil {
				return err
			}
			if err = customfields.ValidateValue(d, v); err != nil {
				return err
			}
		}
		rows, err := tx.Query(ctx, `SELECT name FROM custom_field_definitions WHERE object_type=$1 AND id<>$2`, in.ObjectType, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			if strings.EqualFold(strings.TrimSpace(name), in.Name) {
				rows.Close()
				return &customfields.FieldError{Name: "Name", Message: "already exists for this parent type"}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if id == 0 {
			if err := tx.QueryRow(ctx, `INSERT INTO custom_field_definitions(company_id,object_type,name,field_type,required,options,sort_order,precision,default_value,conversion_key) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,nullif($10,'')) RETURNING id`, p.CompanyID, in.ObjectType, in.Name, in.Type, in.Required, in.Options, in.SortOrder, in.Precision, in.Default, in.ConversionKey).Scan(&id); err != nil {
				return fmt.Errorf("save field: %w", err)
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE custom_field_definitions SET name=$2,field_type=$3,required=$4,options=$5::jsonb,sort_order=$6,precision=$7,default_value=$8,conversion_key=nullif($9,''),updated_at=clock_timestamp() WHERE id=$1`, id, in.Name, in.Type, in.Required, in.Options, in.SortOrder, in.Precision, in.Default, in.ConversionKey); err != nil {
				return err
			}
		}
		return audit(ctx, tx, a, p, "configuration.fields.manage", Configuration, "custom_field", id, map[string]any{"before": before, "after": in})
	})
	return id, err
}

func (s *Service) DeleteField(ctx context.Context, a Actor, id int64) error {
	return s.transaction(ctx, a, false, func(tx pgx.Tx, p Principal) error {
		if !p.Allows("configuration.fields.manage", Configuration) {
			return ErrForbidden
		}
		var name string
		var paired bool
		var before json.RawMessage
		if err := tx.QueryRow(ctx, `SELECT name,conversion_key IS NOT NULL,to_jsonb(d) FROM custom_field_definitions d WHERE id=$1 AND (company_id=$2 OR company_id IS NULL) FOR UPDATE`, id, p.CompanyID).Scan(&name, &paired, &before); err == pgx.ErrNoRows {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		if paired {
			return &customfields.FieldError{ID: id, Name: name, Message: "paired definitions cannot be deleted"}
		}
		if err := customfields.CheckReferences(ctx, tx, id, nil, false); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM custom_field_definitions WHERE id=$1`, id); err != nil {
			return err
		}
		return audit(ctx, tx, a, p, "configuration.fields.manage", Configuration, "custom_field", id, map[string]any{"deleted": name, "before": before})
	})
}
