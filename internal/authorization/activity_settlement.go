package authorization

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Financial facts come from an explicitly recorded immutable operation ID, not
// today's Invoice balance or the current editor. Verify its company and parent
// before projecting the closed fact set. Missing evidence fails the whole query.
func activitySettlementFacts(ctx context.Context, tx pgx.Tx, p Principal, e ActivityRecord, metadata map[string]json.RawMessage) ([]ActivityFact, error) {
	var id string
	if json.Unmarshal(metadata["settlement_operation_id"], &id) != nil || id == "" {
		return nil, nil
	}
	if _, err := uuid.Parse(id); err != nil {
		return nil, fmt.Errorf("invalid retained settlement reference")
	}
	table := ""
	reversal := false
	switch e.Action {
	case "payment_recorded":
		table = "invoice_payments"
	case "credit_applied":
		table = "credit_applications"
	case "credit_refunded":
		table = "credit_refunds"
	case "settlement_reversed", "credit_refund_reversed":
		table = "settlement_reversals"
		reversal = true
	default:
		return nil, nil
	}
	load := func(table, id string) (map[string]json.RawMessage, error) {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(s) FROM `+table+` s WHERE id=$1::uuid AND company_id=$2`, id, p.CompanyID).Scan(&raw); err != nil {
			return nil, fmt.Errorf("retained settlement evidence unavailable: %w", err)
		}
		var row map[string]json.RawMessage
		err := json.Unmarshal(raw, &row)
		return row, err
	}
	row, err := load(table, id)
	if err != nil {
		return nil, err
	}
	owner := row
	facts := []ActivityFact{{Field: "settlement_operation_id", After: id, AfterKnown: true}}
	if reversal {
		var original, kind string
		_ = json.Unmarshal(row["operation_id"], &original)
		_ = json.Unmarshal(row["operation_type"], &kind)
		switch kind {
		case "payment":
			table = "invoice_payments"
		case "credit_application":
			table = "credit_applications"
		case "credit_refund":
			table = "credit_refunds"
		default:
			return nil, fmt.Errorf("unknown retained settlement reversal type")
		}
		owner, err = load(table, original)
		if err != nil {
			return nil, err
		}
		facts = append(facts, ActivityFact{Field: "reverses_operation_id", After: original, AfterKnown: true})
		if amount, ok := activityScalar(owner["amount_cents"]); ok {
			facts = append(facts, ActivityFact{Field: "reversed_amount_cents", After: amount, AfterKnown: true})
		}
	}
	key := "invoice_id"
	if e.Type == "customer" {
		key = "customer_id"
	} else if e.Type != "invoice" {
		return nil, fmt.Errorf("invalid settlement history parent")
	}
	var parent int64
	_ = json.Unmarshal(owner[key], &parent)
	if parent != e.RecordID {
		return nil, fmt.Errorf("settlement history parent does not match")
	}
	for _, key := range []string{"amount_cents", "method", "received_date", "effective_date", "reference", "notes", "reason"} {
		if value, ok := activityScalar(row[key]); ok {
			facts = append(facts, ActivityFact{Field: key, After: value, AfterKnown: true})
		}
	}
	return facts, nil
}
