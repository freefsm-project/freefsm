package handlers

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestConversionBatchCycleAPI(t *testing.T) {
	f := newBatchCommercialFixture(t, "estimate")
	source := f.create(t)
	convert := f.operation(source, "convert", map[string]any{})
	first := batchResults(t, f, convert)[0]
	batchOutcome(t, first, "accepted")
	if first.Receipt["document_type"] != "invoice" || first.Receipt["cycle_id"] == "" {
		t.Fatalf("missing conversion identity: %+v", first)
	}
	if replay := batchResults(t, f, convert)[0]; !reflect.DeepEqual(first, replay) {
		t.Fatal("conversion replay changed original receipt")
	}
	lifecycleCall(t, f.api, f.token, "GET", fmt.Sprintf("/estimates/%d", source.ID), "", 404)
	invoice := f
	invoice.kind = "invoice"
	id := int64(first.Receipt["id"].(float64))
	current := matrixState(t, invoice.read(t, id))
	revert := invoice.operation(current, "revert", map[string]any{"cycle_id": first.Receipt["cycle_id"]}, convert["operation_id"].(string))
	reverted := batchResults(t, invoice, revert)[0]
	batchOutcome(t, reverted, "accepted")
	if reverted.Receipt["id"] != float64(source.ID) || reverted.Receipt["cycle_id"] != first.Receipt["cycle_id"] {
		t.Fatalf("reversion lost original identity: %+v", reverted)
	}
	lifecycleCall(t, f.api, f.token, "GET", fmt.Sprintf("/invoices/%d", id), "", 404)
	second := batchResults(t, f, f.operation(matrixState(t, f.read(t, source.ID)), "convert", map[string]any{}))[0]
	batchOutcome(t, second, "accepted")
	if second.Receipt["id"] == first.Receipt["id"] || second.Receipt["cycle_id"] == first.Receipt["cycle_id"] || second.Receipt["number"] == first.Receipt["number"] {
		t.Fatal("reconversion reused cycle, Invoice or Number")
	}
	if replay := batchResults(t, invoice, revert)[0]; !reflect.DeepEqual(reverted, replay) {
		t.Fatal("later cycle changed original reversion receipt")
	}
}

func TestConversionBatchDependenciesAPI(t *testing.T) {
	for _, scenario := range []string{"stale", "unknown", "transient"} {
		t.Run(scenario, func(t *testing.T) {
			f := newBatchCommercialFixture(t, "estimate")
			a, b, c := f.create(t), f.create(t), f.create(t)
			parent := f.operation(a, "convert", map[string]any{})
			child := f.operation(b, "convert", map[string]any{}, parent["operation_id"].(string))
			unrelated := f.operation(c, "convert", map[string]any{})
			want := "stale_commercial_revision"
			switch scenario {
			case "stale":
				batchOutcome(t, batchResults(t, f, f.operation(a, "edit", map[string]any{"notes": "Intervening edit"}))[0], "accepted")
			case "unknown":
				parent["depends_on"] = []string{newOperationKey()}
				want = "invalid_dependency"
			case "transient":
				_, err := f.pool.Exec(context.Background(), fmt.Sprintf(`CREATE FUNCTION conversion_batch_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation_id='%s'::uuid THEN RAISE EXCEPTION 'conversion receipt failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER conversion_batch_failure BEFORE INSERT ON commercial_operations FOR EACH ROW EXECUTE FUNCTION conversion_batch_failure()`, parent["operation_id"]))
				historyCheck(t, err)
			}
			results := batchResults(t, f, parent, child, unrelated)
			if scenario == "transient" {
				if results[0].Error.Code != "retryable_failure" || results[0].Receipt != nil {
					t.Fatalf("unknown parent: %+v", results[0])
				}
				_, err := f.pool.Exec(context.Background(), `DROP TRIGGER conversion_batch_failure ON commercial_operations`)
				historyCheck(t, err)
			} else {
				batchOutcome(t, results[0], want)
			}
			batchOutcome(t, results[1], "failed_dependency")
			batchOutcome(t, results[2], "accepted")
			f.read(t, a.ID)
			f.read(t, b.ID)
			if scenario != "transient" {
				parent = f.operation(matrixState(t, f.read(t, a.ID)), "convert", map[string]any{})
			}
			batchOutcome(t, batchResults(t, f, parent)[0], "accepted")
			batchOutcome(t, batchResults(t, f, child)[0], "failed_dependency")
			child["operation_id"] = newOperationKey()
			child["depends_on"] = []string{parent["operation_id"].(string)}
			batchOutcome(t, batchResults(t, f, child)[0], "accepted")
		})
	}
}

func TestConversionBatchMalformedAPI(t *testing.T) {
	f := newBatchCommercialFixture(t, "estimate")
	state := f.create(t)
	for _, scenario := range []string{"null_payload", "null_revision", "null_fields", "unknown", "nested_identity", "convert_cycle", "revert_fields", "wrong_direction"} {
		t.Run(scenario, func(t *testing.T) {
			prefix := f.operation(state, "convert", map[string]any{})
			op := f.operation(state, "convert", map[string]any{})
			fields := op["commercial"].(map[string]any)
			switch scenario {
			case "null_payload":
				op["commercial"] = nil
			case "null_revision":
				fields["revision"] = nil
			case "null_fields":
				fields["custom_fields"] = nil
			case "unknown":
				fields["force"] = true
			case "nested_identity":
				fields["operation_id"] = newOperationKey()
			case "convert_cycle":
				fields["cycle_id"] = newOperationKey()
			case "revert_fields":
				op["type"] = "invoice.revert"
				fields["custom_fields"] = []any{}
			case "wrong_direction":
				op["type"] = "invoice.convert"
			}
			commercialBatchHTTP(t, f.api, f.token, matrixBody(t, map[string]any{"operations": []any{prefix, op}}), 400)
			f.read(t, state.ID)
		})
	}
}
