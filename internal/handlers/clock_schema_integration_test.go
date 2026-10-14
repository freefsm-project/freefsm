package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/google/uuid"
)

func clockReviewCall(t *testing.T, server *httptest.Server, token, method, path string, body any) (int, string) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if method == "POST" && path == "/time-entries/lease" {
		req.Header.Set("Operation-ID", uuid.NewString())
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

// Retains all eight F58-1 requests, plus every other non-nullable property
// and exact-case/unknown-property checks at both levels of the served schema.
func TestClockSchemaInvalidFieldsNeverCommitHTTP(t *testing.T) {
	invalid := map[string]map[string]any{}
	for _, field := range []string{"operation_id", "entry_id", "job_id", "notes", "occurred_at", "lease_id", "gps"} {
		invalid[field+"-null"] = map[string]any{field: nil}
	}
	invalid["entry-zero"] = map[string]any{"entry_id": 0}
	invalid["lease-empty"] = map[string]any{"lease_id": ""}
	invalid["unknown"] = map[string]any{"extra": true}
	invalid["case-job"] = map[string]any{"JOB_ID": 1}
	invalid["case-notes"] = map[string]any{"Notes": "discarded?"}
	invalid["case-operation"] = map[string]any{"Operation_ID": uuid.NewString()}
	for field, value := range map[string]any{"Entry_ID": 1, "Occurred_at": "2026-01-01T00:00:00Z", "Lease_ID": uuid.NewString(), "GPS": map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": "2026-01-01T00:00:00Z"}} {
		invalid["case-"+field] = map[string]any{field: value}
	}
	for field, value := range map[string]any{"Latitude": 51.5, "Longitude": -0.12, "Captured_at": "2026-01-01T00:00:00Z", "Accuracy": 0, "extra": true} {
		gps := map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": "2026-01-01T00:00:00Z", field: value}
		invalid["gps-case-"+field] = map[string]any{"gps": gps}
	}
	for _, field := range []string{"latitude", "longitude", "captured_at", "accuracy", "Latitude", "extra"} {
		gps := map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": "2026-01-01T00:00:00Z"}
		gps[field] = nil
		invalid["gps-"+field] = map[string]any{"gps": gps}
	}
	for _, kind := range []string{"in", "out"} {
		t.Run(kind, func(t *testing.T) {
			pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
			ctx := context.Background()
			token, err := sessions.CreateMobile(ctx, admin, "clock schema")
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
			defer server.Close()
			status, schemaBody := clockReviewCall(t, server, token, "GET", "/openapi.json", nil)
			var schema struct {
				Components struct {
					Schemas map[string]struct {
						Properties map[string]json.RawMessage `json:"properties"`
					} `json:"schemas"`
				} `json:"components"`
			}
			if status != 200 {
				t.Fatalf("served schema=%d %s", status, schemaBody)
			}
			if err := json.Unmarshal([]byte(schemaBody), &schema); err != nil {
				t.Fatal(err)
			}
			for field := range schema.Components.Schemas["ClockCommand"].Properties {
				if _, ok := invalid[field+"-null"]; !ok {
					t.Fatalf("missing supplied-null conformance for served property %s", field)
				}
			}
			var entryID int64
			if kind == "out" {
				status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString()})
				if status != 200 {
					t.Fatalf("genuine omission control: %d %s", status, body)
				}
				entryID = client.TimeEntry.Query().OnlyX(ctx).ID
			}
			for name, fields := range invalid {
				t.Run(name, func(t *testing.T) {
					op := uuid.NewString()
					command := map[string]any{"operation_id": op}
					if kind == "out" {
						command["entry_id"] = entryID
					}
					for key, value := range fields {
						command[key] = value
					}
					status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-"+kind, command)
					if status != 400 {
						t.Errorf("schema-invalid request=%v: %d %s", command, status, body)
					}
					var accepted bool
					if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM time_clock_receipts WHERE operation_id=$1 AND code='accepted')`, op).Scan(&accepted); err != nil {
						t.Fatal(err)
					}
					entries := client.TimeEntry.Query().AllX(ctx)
					if accepted || kind == "in" && len(entries) != 0 || kind == "out" && (len(entries) != 1 || entries[0].ClockOut != nil) {
						t.Fatalf("invalid input committed clock or receipt: %s", body)
					}
				})
			}
		})
	}
}

func TestClockJSONObjectShapeHTTP(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "clock JSON shape")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
	defer server.Close()
	for _, raw := range []string{
		`null`, `[]`, `{}`,
		`{"operation_id":"58000000-0000-4000-8000-000000000001","job_id":1,"job_id":2}`,
		`{"operation_id":"58000000-0000-4000-8000-000000000002","gps":{"latitude":51.5,"latitude":52.5,"longitude":-0.12,"captured_at":"2026-01-01T00:00:00Z"}}`,
		`{"operation_id":"58000000-0000-4000-8000-000000000003","notes":123}`,
		`{"operation_id":"58000000-0000-4000-8000-000000000004","gps":[]}`,
	} {
		before := clockEffectSnapshot(t, pool)
		status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-in", json.RawMessage(raw))
		if status != 400 {
			t.Errorf("invalid JSON shape %s: %d %s", raw, status, body)
		}
		if after := clockEffectSnapshot(t, pool); after != before {
			t.Fatal("schema-invalid shape committed effects")
		}
	}
}
