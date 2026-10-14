package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/google/uuid"
)

// F58-3: capture, occurrence and acceptance are independently known facts.
// Check the canonical column AND the protected browser/CSV projection.
func TestClockGPSAuditOccurrenceHTTP(t *testing.T) {
	pool, client, router, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "GPS evidence device")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	status, body := clockReviewCall(t, server, token, "POST", "/time-entries/lease", map[string]any{})
	var lease struct {
		Data authorization.ClockLease `json:"data"`
	}
	if status != 200 {
		t.Fatalf("lease=%d %s", status, body)
	}
	if err := json.Unmarshal([]byte(body), &lease); err != nil {
		t.Fatal(err)
	}
	want := map[string]time.Time{}
	for _, kind := range []string{"in", "out"} {
		time.Sleep(10 * time.Millisecond)
		occurrence := time.Now().UTC().Truncate(time.Microsecond)
		capture := occurrence.Add(-time.Millisecond)
		op := uuid.NewString()
		want[op] = occurrence
		command := map[string]any{"operation_id": op, "lease_id": lease.Data.ID, "occurred_at": occurrence, "gps": map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": capture}}
		if kind == "out" {
			command["entry_id"] = client.TimeEntry.Query().OnlyX(ctx).ID
		}
		time.Sleep(10 * time.Millisecond)
		status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-"+kind, command)
		if status != 200 {
			t.Fatalf("%s=%d %s", kind, status, body)
		}
		var recorded, captured *time.Time
		var accepted time.Time
		if err := pool.QueryRow(ctx, `SELECT occurred_at,(detail->>'captured_at')::timestamptz,(detail->>'accepted_at')::timestamptz FROM authorization_audit WHERE action='time.gps.capture' AND detail->>'operation_id'=$1`, op).Scan(&recorded, &captured, &accepted); err != nil {
			t.Fatal(err)
		}
		if recorded == nil || !recorded.Equal(occurrence) || captured == nil || !captured.Equal(capture) || !accepted.After(occurrence) {
			t.Errorf("%s occurrence=%v capture=%v acceptance=%v; want occurrence %v capture %v", kind, recorded, captured, accepted, occurrence, capture)
		}
	}
	cookie := sessionCookie(t, ctx, sessions, admin)
	body = requestBody(t, router, cookie, "GET", "/settings/audit?permission=time.gps.capture&channel=api", 200)
	for op, occurrence := range want {
		assertContains(t, body, op)
		assertContains(t, body, occurrence.Format(time.RFC3339Nano))
	}
	path := masterPost(t, router, cookie, "/settings/audit/export", url.Values{"operation_id": {newOperationKey()}, "permission": {"time.gps.capture"}}, 303)
	if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	body = requestBody(t, router, cookie, "GET", path+"/download", 200)
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected two GPS events: %#v", rows)
	}
	for _, row := range rows[1:] {
		occurrence, ok := want[row[21]]
		if !ok || row[22] != occurrence.Format(time.RFC3339Nano) || row[19] != "GPS evidence device" {
			t.Errorf("GPS audit CSV facts: %#v", row)
		}
	}
}
