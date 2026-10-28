package customfields

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
)

// Preflight inspects migration 058's recorded values, including archives,
// tombstones and immutable conversion snapshots. Its historical parent set must
// not grow with the runtime catalog: an unknown legacy Lease store belongs to
// migration 103's explicit mapping review, not an inferred native field schema.
// Diagnostics name identities, never echo the record's entered values.
func Preflight(ctx context.Context, tx pgx.Tx) error {
	tables := []string{"custom_field_definitions", "estimate_invoice_conversion_cycles"}
	installedParents := []Parent{{"customer", "customers"}, {"project", "projects"}, {"job", "jobs"}, {"asset", "assets"}, {"estimate", "estimates"}, {"invoice", "invoices"}}
	for _, p := range installedParents {
		tables = append(tables, p.Table)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE `+strings.Join(tables, ",")+` IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id,object_type,name,field_type,options::text,(to_jsonb(d)->>'precision')::integer FROM custom_field_definitions d ORDER BY id`)
	if err != nil {
		return err
	}
	defs := map[int64]Definition{}
	parents := map[int64]string{}
	names := map[string]bool{}
	var review []string
	for rows.Next() {
		var d Definition
		var typ string
		if err := rows.Scan(&d.ID, &typ, &d.Name, &d.Type, &d.Options, &d.Precision); err != nil {
			rows.Close()
			return err
		}
		defs[d.ID] = d
		parents[d.ID] = typ
		reason := ""
		if !SupportsParent(typ) {
			reason = "unsupported parent type"
		}
		if !SupportsType(d.Type) {
			reason = "unknown field type"
		}
		if d.Type == "number" && (d.Precision == nil || *d.Precision < 0 || *d.Precision > 18) {
			reason = "explicit numeric precision review is required"
		}
		key := typ + ":" + nameKey(d.Name)
		if names[key] || strings.TrimSpace(d.Name) == "" {
			reason = "ambiguous normalized name"
		}
		names[key] = true
		if _, err := Options(d.Options); err != nil {
			reason = "ambiguous option encoding or duplicate option names"
		}
		if reason != "" {
			review = append(review, fmt.Sprintf("definition %d: %s", d.ID, reason))
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, parent := range installedParents {
		typ := parent.Type
		rows, err := tx.Query(ctx, `SELECT id,custom_fields::text FROM `+parent.Table+` ORDER BY id`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var raw string
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return err
			}
			if reason := reviewValues(raw, typ, defs, parents); reason != "" {
				review = append(review, fmt.Sprintf("%s %d: %s", typ, id, reason))
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	rows, err = tx.Query(ctx, `SELECT id::text,(source_snapshot->'custom_fields')::text FROM estimate_invoice_conversion_cycles ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return err
		}
		if reason := reviewValues(raw, "estimate", defs, parents); reason != "" {
			review = append(review, "conversion snapshot "+id+": "+reason)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(review) > 0 {
		return fmt.Errorf("custom fields require explicit review before migration: %s", strings.Join(review, "; "))
	}
	return nil
}

func reviewValues(raw, typ string, defs map[int64]Definition, parents map[int64]string) string {
	var values []Value
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&values) != nil || values == nil {
		return "ambiguous value encoding"
	}
	seen := map[int64]bool{}
	for _, v := range values {
		d, ok := defs[v.DefinitionID]
		if !ok || parents[v.DefinitionID] != typ || seen[v.DefinitionID] {
			return fmt.Sprintf("definition %d is missing, repeated or belongs to another parent", v.DefinitionID)
		}
		seen[v.DefinitionID] = true
		if v.Value == nil {
			return fmt.Sprintf("definition %d has an ambiguous null value", d.ID)
		}
		if err := ValidateValue(d, v.Value); err != nil {
			return fmt.Sprintf("definition %d has an incompatible recorded value", d.ID)
		}
	}
	return ""
}
