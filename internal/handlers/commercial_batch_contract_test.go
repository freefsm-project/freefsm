package handlers

import (
	"context"
	"testing"
)

func TestCommercialBatchMalformedAPI(t *testing.T) {
	f := newBatchCommercialFixture(t, "estimate")
	state := f.create(t)
	for _, name := range []string{"omitted_payload", "null_payload", "array_payload", "unknown_payload", "nested_operation", "null_notes", "null_status", "null_dependency", "omitted_dependencies", "null_dependencies"} {
		t.Run(name, func(t *testing.T) {
			valid := f.operation(state, "edit", map[string]any{"notes": "Must not execute"})
			op := f.operation(state, "edit", map[string]any{"notes": "Invalid"})
			switch name {
			case "omitted_payload":
				delete(op, "commercial")
			case "null_payload":
				op["commercial"] = nil
			case "array_payload":
				op["commercial"] = []any{}
			case "unknown_payload":
				op["commercial"].(map[string]any)["force"] = true
			case "nested_operation":
				op["commercial"].(map[string]any)["operation_id"] = newOperationKey()
			case "null_notes":
				op["commercial"].(map[string]any)["notes"] = nil
			case "null_status":
				op["type"], op["commercial"] = "estimate.status", map[string]any{"status_id": nil}
			case "null_dependency":
				op["depends_on"] = []any{nil}
			case "omitted_dependencies":
				delete(op, "depends_on")
			case "null_dependencies":
				op["depends_on"] = nil
			}
			commercialBatchHTTP(t, f.api, f.token, matrixBody(t, map[string]any{"operations": []any{valid, op}}), 400)
			var count int
			historyCheck(t, f.pool.QueryRow(context.Background(), `SELECT count(*) FROM operation_admissions WHERE operation_id=$1 OR operation_id=$2`, valid["operation_id"], op["operation_id"]).Scan(&count))
			if count != 0 {
				t.Fatal("invalid envelope admitted prefix")
			}
		})
	}
	// Missing domain inputs are terminal, unlike malformed JSON; omission is not
	// silently filled from current state. Optional edit fields remain unchanged.
	op := f.operation(state, "edit", map[string]any{})
	op["commercial"] = map[string]any{}
	batchOutcome(t, batchResults(t, f, op)[0], "invalid_document")
}
