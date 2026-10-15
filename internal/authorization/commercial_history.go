package authorization

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type CommercialFieldChange struct{ Field, Before, After string }
type CommercialChange struct {
	RecordedAt time.Time
	Actor      string
	Fields     []CommercialFieldChange
}

// CommercialHistory exposes only the parent-bound operational fields of the
// immutable evidence, never Role/grant/session information or sealed events.
func (s *Service) CommercialHistory(ctx context.Context, a Actor, kind string, id int64) ([]CommercialChange, error) {
	result := []CommercialChange{}
	if kind != "estimate" && kind != "invoice" {
		return nil, ErrNotFound
	}
	err := s.WithinParent(ctx, a, kind, id, "", func(tx pgx.Tx, p Principal) error {
		var job int64
		if err := tx.QueryRow(ctx, `SELECT coalesce(job_id,0) FROM `+kind+`s WHERE id=$1`, id).Scan(&job); err != nil {
			return err
		}
		assigned, err := assignedJobAccess(ctx, tx, p.UserID, job, true)
		if err != nil {
			return err
		}
		pricing := p.Allows("pricing.view", All) || assigned && p.Allows("pricing.view", Assigned)
		rows, err := tx.Query(ctx, `SELECT recorded_at,coalesce(actor_name,'Recorded actor'),detail FROM authorization_audit WHERE target_type=$1 AND target_id=$2 AND action IN ($1||'.edit','job.edit') AND detail ? 'before' AND detail ? 'after' ORDER BY id DESC`, kind, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var change CommercialChange
			var detail struct{ Before, After json.RawMessage }
			var raw []byte
			if err := rows.Scan(&change.RecordedAt, &change.Actor, &raw); err != nil {
				return err
			}
			if err := json.Unmarshal(raw, &detail); err != nil {
				return err
			}
			before, err := commercialHistoryValues(detail.Before)
			if err != nil {
				return err
			}
			after, err := commercialHistoryValues(detail.After)
			if err != nil {
				return err
			}
			for _, field := range []struct{ key, label string }{{"title", "Title"}, {"notes", "Notes"}, {"terms", "Terms"}, {"invoice_date", "Invoice Date"}, {"due_date", "Due Date"}, {"valid_through", "Valid Through"}, {"customer_id", "Customer"}, {"job_id", "Job"}, {"line_items", "Lines"}, {"financial", "Accepted amounts and adjustments"}, {"tax_rate", "Tax Rate"}, {"custom_fields", "Custom Fields"}} {
				if !pricing && (field.key == "line_items" || field.key == "tax_rate" || field.key == "financial") {
					continue
				}
				if field.key == "customer_id" && !p.Allows("customer.view", All) || field.key == "job_id" && !p.Allows("job.view", All) {
					continue
				}
				if bytes.Equal(before[field.key], after[field.key]) {
					continue
				}
				if field.key == "custom_fields" {
					change.Fields = append(change.Fields, commercialFieldValueChanges(before[field.key], after[field.key])...)
				} else {
					change.Fields = append(change.Fields, CommercialFieldChange{field.label, commercialHistoryText(field.key, before[field.key]), commercialHistoryText(field.key, after[field.key])})
				}
			}
			// Filter before returning entries: a pricing-only event must not
			// reveal even its existence to a user without captured pricing View.
			if len(change.Fields) > 0 {
				result = append(result, change)
			}
		}
		return rows.Err()
	})
	return result, err
}

func commercialHistoryValues(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		return map[string]json.RawMessage{"custom_fields": json.RawMessage(encoded)}, nil
	}
	var fields map[string]json.RawMessage
	err := json.Unmarshal(raw, &fields)
	return fields, err
}

func commercialHistoryText(field string, raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return "None"
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return "Empty"
		}
		if field == "due_date" && text == "0001-01-01" {
			return "No Due Date"
		}
		return text
	}
	if field == "line_items" {
		var lines []map[string]json.RawMessage
		if json.Unmarshal(raw, &lines) == nil {
			for _, line := range lines {
				for key := range line {
					switch key {
					case "item_id", "title", "description", "unit_price", "quantity", "taxable", "tax_rate", "discount", "surcharge", "unit_price_decimal", "quantity_decimal", "discount_adjustment", "surcharge_adjustment":
					default:
						delete(line, key)
					}
				}
			}
			raw, _ = json.Marshal(lines)
		}
	}
	var out bytes.Buffer
	if json.Indent(&out, raw, "", "  ") == nil {
		return out.String()
	}
	return "Recorded value unavailable"
}

func commercialFieldValueChanges(before, after json.RawMessage) []CommercialFieldChange {
	decode := func(raw json.RawMessage) map[int64]json.RawMessage {
		var values []struct {
			DefinitionID int64           `json:"definition_id"`
			Value        json.RawMessage `json:"value"`
		}
		_ = json.Unmarshal(raw, &values)
		out := map[int64]json.RawMessage{}
		for _, v := range values {
			out[v.DefinitionID] = v.Value
		}
		return out
	}
	a, b := decode(before), decode(after)
	ids := map[int64]bool{}
	for id := range a {
		ids[id] = true
	}
	for id := range b {
		ids[id] = true
	}
	ordered := []int64{}
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	var fields []CommercialFieldChange
	for _, id := range ordered {
		if !bytes.Equal(a[id], b[id]) {
			fields = append(fields, CommercialFieldChange{fmt.Sprintf("Custom Field %d", id), commercialHistoryText("", a[id]), commercialHistoryText("", b[id])})
		}
	}
	return fields
}
