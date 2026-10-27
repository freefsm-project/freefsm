package handlers

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// #54 preserves original PDF/Delivery identity and payload; #56 makes a hidden
// original ineligible for future dispatch. Permit only the exact cancellation
// lifecycle delta, and verify its appended event without dropping old history.
func conversionCancelledDeliveries(t *testing.T, pool *pgxpool.Pool, before map[string]string, kind string, id int64, started time.Time) map[string]string {
	t.Helper()
	ctx := context.Background()
	after := historyDurableRows(t, pool, []string{"document_pdf_versions", "document_deliveries", "document_delivery_events"})
	historyEqual(t, "conversion preserves every PDF field", after["document_pdf_versions"], before["document_pdf_versions"])
	parse := func(raw string) map[string]map[string]any {
		var rows []map[string]any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		historyCheck(t, decoder.Decode(&rows))
		out := map[string]map[string]any{}
		for _, row := range rows {
			key := row["id"].(json.Number).String()
			if out[key] != nil {
				t.Fatal("duplicate persisted identity")
			}
			out[key] = row
		}
		return out
	}
	wanted, actual := parse(before["document_deliveries"]), parse(after["document_deliveries"])
	if len(wanted) != len(actual) {
		t.Fatal("conversion created or removed Delivery identities")
	}
	cancelled := map[string]map[string]any{}
	const reason = "Original document or Customer is archived, hidden, or Void"
	for key, old := range wanted {
		numericID, err := strconv.ParseInt(key, 10, 64)
		historyCheck(t, err)
		current := actual[key]
		if current == nil {
			t.Fatal("original Delivery missing", key)
		}
		if old["document_type"] == kind && old["document_id"].(json.Number).String() == strconv.FormatInt(id, 10) && old["state"] == "queued" {
			if old["handoff_started_at"] != nil || old["dispatch_boundary_known"] != true {
				t.Fatal("fixture must represent known pre-handoff work")
			}
			var inWindow bool
			historyCheck(t, pool.QueryRow(ctx, `SELECT cancelled_at>=$2 AND cancelled_at<=clock_timestamp() AND updated_at>=$2 AND updated_at<=clock_timestamp() FROM document_deliveries WHERE id=$1`, numericID, started).Scan(&inWindow))
			if !inWindow {
				t.Fatal("cancellation must record the current conversion boundary")
			}
			old["state"] = "cancelled"
			old["last_error"] = reason
			old["cancelled_at"] = current["cancelled_at"]
			old["updated_at"] = current["updated_at"]
			cancelled[key] = old
		}
		historyEqual(t, "all original Delivery fields except verified cancellation metadata: "+key, current, old)
	}
	if len(cancelled) == 0 {
		t.Fatal("fixture did not exercise pre-handoff cancellation")
	}
	oldEvents, newEvents := parse(before["document_delivery_events"]), parse(after["document_delivery_events"])
	if len(newEvents) != len(oldEvents)+len(cancelled) {
		t.Fatal("missing/extra cancellation or historical event")
	}
	var lastID int64
	for key, event := range oldEvents {
		n, err := strconv.ParseInt(key, 10, 64)
		historyCheck(t, err)
		lastID = max(lastID, n)
		historyEqual(t, "original immutable event "+key, newEvents[key], event)
	}
	seen := map[string]bool{}
	for key, event := range newEvents {
		if oldEvents[key] != nil {
			continue
		}
		n, err := strconv.ParseInt(key, 10, 64)
		historyCheck(t, err)
		if n <= lastID {
			t.Fatal("cancellation reused an event identity")
		}
		deliveryID := event["delivery_id"].(json.Number).String()
		record := cancelled[deliveryID]
		if record == nil || seen[deliveryID] {
			t.Fatal("unexpected or duplicated cancellation target")
		}
		seen[deliveryID] = true
		var inWindow bool
		historyCheck(t, pool.QueryRow(ctx, `SELECT created_at>=$2 AND created_at<=clock_timestamp() FROM document_delivery_events WHERE id=$1`, n, started).Scan(&inWindow))
		if !inWindow {
			t.Fatal("cancellation event backdated")
		}
		expected := map[string]any{"id": event["id"], "delivery_id": record["id"], "company_id": record["company_id"], "actor_id": nil, "event_kind": "state", "from_state": "queued", "to_state": "cancelled", "provider_identifier": "", "provider_event_id": nil, "evidence": map[string]any{"reason": reason, "dispatch_admitted": false}, "created_at": event["created_at"]}
		// Pre-handoff cancellation has no transport/provider attempt evidence.
		// Require actual NULLs in migration 099's columns; retain exact row equality.
		expected["attempt_token"] = nil
		expected["message_id"] = nil
		expected["provider_state"] = nil
		expected["transport_event_id"] = nil
		historyEqual(t, "exact cancellation event", event, expected)
	}
	return after
}
