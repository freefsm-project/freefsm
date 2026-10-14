package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func clockEffectSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var result string
	if err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object(
 'entries',(SELECT coalesce(jsonb_agg(to_jsonb(e) ORDER BY id),'[]') FROM time_entries e),
 'gps',(SELECT coalesce(jsonb_agg(to_jsonb(g) ORDER BY entry_id,event),'[]') FROM time_clock_gps g),
 'audit',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]') FROM authorization_audit a WHERE action LIKE 'time.%'),
 'activity',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]') FROM activity_logs a WHERE action IN ('clocked_in','clocked_out'))
)::text`).Scan(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestClockLeaseGPSAndOrderBoundariesHTTP(t *testing.T) {
	for _, kind := range []string{"in", "out"} {
		for _, scenario := range []string{"before-lease", "after-lease", "other-device", "gps-before-lease", "gps-after-lease", "gps-future", "negative-accuracy", "longitude-range", "out-equal-in", "out-before-in"} {
			if kind == "in" && (scenario == "out-equal-in" || scenario == "out-before-in") {
				continue
			}
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				token, err := sessions.CreateMobile(ctx, admin, "clock bounds")
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
				defer server.Close()
				// An explicit historical staging fixture, not fabricated application
				// history: test instants are strictly within the real 24h lease length.
				issued := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Microsecond)
				expires := issued.Add(24 * time.Hour)
				occurrence := issued.Add(2 * time.Hour)
				lease := uuid.NewString()
				key := fmt.Sprintf("api:%x", sha256.Sum256([]byte(token)))
				if _, err := pool.Exec(ctx, `INSERT INTO time_clock_leases(id,user_id,client_key,issued_at,expires_at) VALUES($1,$2,$3,$4,$5)`, lease, admin, key, issued, expires); err != nil {
					t.Fatal(err)
				}
				command := map[string]any{"operation_id": uuid.NewString(), "lease_id": lease, "occurred_at": occurrence}
				start := issued.Add(time.Hour)
				if kind == "out" {
					status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString(), "lease_id": lease, "occurred_at": start})
					if status != 200 {
						t.Fatalf("valid staged control=%d %s", status, body)
					}
					command["entry_id"] = client.TimeEntry.Query().OnlyX(ctx).ID
				}
				gps := map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": occurrence, "accuracy": 0}
				command["gps"] = gps
				wantCode, wantStatus := "invalid_gps", 400
				switch scenario {
				case "before-lease":
					command["occurred_at"] = issued.Add(-time.Microsecond)
					wantCode = "invalid_lease"
				case "after-lease":
					command["occurred_at"] = expires.Add(time.Microsecond)
					wantCode = "invalid_lease"
				case "other-device":
					other, err := sessions.CreateMobile(ctx, admin, "other staging device")
					if err != nil {
						t.Fatal(err)
					}
					token = other
					wantCode = "invalid_lease"
				case "gps-before-lease":
					gps["captured_at"] = issued.Add(-time.Microsecond)
				case "gps-after-lease":
					gps["captured_at"] = expires.Add(time.Microsecond)
				case "gps-future":
					// No offline lease: isolate the acceptance-time upper bound.
					delete(command, "lease_id")
					delete(command, "occurred_at")
					gps["captured_at"] = time.Now().Add(time.Hour).UTC().Truncate(time.Microsecond)
				case "negative-accuracy":
					gps["accuracy"] = -0.1
				case "longitude-range":
					gps["longitude"] = 180.01
				case "out-equal-in":
					command["occurred_at"] = start
					wantCode, wantStatus = "time_order", 409
				case "out-before-in":
					command["occurred_at"] = start.Add(-time.Microsecond)
					wantCode, wantStatus = "time_order", 409
				}
				before := clockEffectSnapshot(t, pool)
				for range 2 {
					status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-"+kind, command)
					var response struct {
						Error struct {
							Code string `json:"code"`
						} `json:"error"`
					}
					if err := json.Unmarshal([]byte(body), &response); err != nil {
						t.Fatal(err)
					}
					if status != wantStatus || response.Error.Code != wantCode {
						t.Fatalf("want %d/%s: %d %s", wantStatus, wantCode, status, body)
					}
				}
				if after := clockEffectSnapshot(t, pool); after != before {
					t.Fatalf("rejected/replayed request changed effects: before=%s after=%s", before, after)
				}
				status, body := clockReviewCall(t, server, token, "GET", "/time-operations/"+command["operation_id"].(string), nil)
				if status != 200 {
					t.Fatalf("terminal receipt=%d %s", status, body)
				}
				var receipt struct {
					Data struct {
						Code    string `json:"code"`
						EntryID int64  `json:"entry_id"`
					} `json:"data"`
				}
				if err := json.Unmarshal([]byte(body), &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Data.Code != wantCode || receipt.Data.EntryID != 0 {
					t.Fatalf("terminal failure receipt=%s", body)
				}
			})
		}
	}
}

func TestClockEachAuditFailureRollsBackHTTP(t *testing.T) {
	for _, kind := range []string{"in", "out"} {
		for _, event := range []string{"time.gps.capture", "time.clock_" + kind} {
			t.Run(kind+"/"+event, func(t *testing.T) {
				pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
				ctx := context.Background()
				token, err := sessions.CreateMobile(ctx, admin, "audit rollback")
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(apiv1.NewRouter(pool, client, sessions))
				defer server.Close()
				command := map[string]any{"operation_id": uuid.NewString(), "gps": map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": time.Now().UTC().Truncate(time.Microsecond)}}
				if kind == "out" {
					status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString()})
					if status != 200 {
						t.Fatalf("control=%d %s", status, body)
					}
					command["entry_id"] = client.TimeEntry.Query().OnlyX(ctx).ID
				}
				if _, err := pool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION reject_clock_evidence() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='%s' THEN RAISE EXCEPTION 'injected clock audit'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_clock_evidence BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_clock_evidence()`, event)); err != nil {
					t.Fatal(err)
				}
				before := clockEffectSnapshot(t, pool)
				status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-"+kind, command)
				if status != 503 {
					t.Fatalf("audit failure=%d %s", status, body)
				}
				if after := clockEffectSnapshot(t, pool); after != before {
					t.Fatalf("partial effects before=%s after=%s", before, after)
				}
				status, body = clockReviewCall(t, server, token, "GET", "/time-operations/"+command["operation_id"].(string), nil)
				if status != 404 {
					t.Fatalf("transient failure acquired receipt=%d %s", status, body)
				}
				if _, err := pool.Exec(ctx, `DROP TRIGGER reject_clock_evidence ON authorization_audit`); err != nil {
					t.Fatal(err)
				}
				for range 2 {
					status, body = clockReviewCall(t, server, token, "POST", "/time-entries/clock-"+kind, command)
					if status != 200 {
						t.Fatalf("same-ID retry=%d %s", status, body)
					}
				}
				var gps, audit int
				if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM time_clock_gps),(SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1)`, command["operation_id"]).Scan(&gps, &audit); err != nil {
					t.Fatal(err)
				}
				if gps != 1 || audit != 2 || client.TimeEntry.Query().CountX(ctx) != 1 {
					t.Fatalf("retry duplicated effects: gps=%d audit=%d", gps, audit)
				}
			})
		}
	}
}
