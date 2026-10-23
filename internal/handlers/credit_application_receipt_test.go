package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// The response is deliberately exact: effect identity is retained, but no
// Invoice, source, amount, reason, note or current projection is disclosed.
func assertCreditApplicationReceipt(t *testing.T, response *httptest.ResponseRecorder, operation, effect, kind string) {
	t.Helper()
	var got map[string]map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]string{"data": {"operation_id": operation, "outcome": "accepted", "effect_id": effect, "effect_type": kind}}
	historyEqual(t, "retained committed effect identity without sensitive projection", got, want)
}
