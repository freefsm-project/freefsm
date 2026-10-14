package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1 "github.com/freefsm-project/freefsm/internal/api/v1"
	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
)

func TestClockCorrectionPreservesGPSAndRacesNewClock(t *testing.T) {
	pool, client, _, sessions, _, admin := permissionHTTPFixture(t)
	ctx := context.Background()
	token, err := sessions.CreateMobile(ctx, admin, "correction race")
	if err != nil {
		t.Fatal(err)
	}
	api := apiv1.NewRouter(pool, client, sessions)
	server := httptest.NewServer(api)
	defer server.Close()
	start := time.Now().Add(-3 * time.Hour).UTC().Truncate(time.Microsecond)
	end := start.Add(time.Hour)
	lease := uuid.NewString()
	key := fmt.Sprintf("api:%x", sha256.Sum256([]byte(token)))
	if _, err := pool.Exec(ctx, `INSERT INTO time_clock_leases(id,user_id,client_key,issued_at,expires_at) VALUES($1,$2,$3,$4,$5)`, lease, admin, key, start.Add(-time.Hour), start.Add(23*time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"in", "out"} {
		occurrence := start
		command := map[string]any{"operation_id": uuid.NewString(), "lease_id": lease}
		if kind == "out" {
			occurrence = end
			command["entry_id"] = client.TimeEntry.Query().OnlyX(ctx).ID
		}
		command["occurred_at"] = occurrence
		command["gps"] = map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": occurrence.Add(-time.Second), "accuracy": 3}
		status, body := clockReviewCall(t, server, token, "POST", "/time-entries/clock-"+kind, command)
		if status != 200 {
			t.Fatalf("clock control=%d %s", status, body)
		}
	}
	entry := client.TimeEntry.Query().OnlyX(ctx)
	var originalGPS string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g) ORDER BY event)::text FROM time_clock_gps g WHERE entry_id=$1`, entry.ID).Scan(&originalGPS); err != nil {
		t.Fatal(err)
	}
	svc := authorization.New(pool)
	actor := authorization.Actor{UserID: admin, Channel: "web"}
	correctedStart := start.Add(-time.Minute)
	notes := "reviewed correction, GPS remains recorded evidence"
	if err := svc.CorrectTimeEntry(ctx, actor, entry.ID, authorization.TimeEntryCorrection{ClockIn: &correctedStart, Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	corrected := client.TimeEntry.GetX(ctx, entry.ID)
	if !corrected.ClockIn.Equal(correctedStart) || corrected.UserID != admin || corrected.Notes != notes {
		t.Fatalf("correction not persisted: %+v", corrected)
	}
	// A clock staged between the old and proposed corrected end overlaps iff
	// that correction wins the shared commit fence. Exactly one may succeed.
	newStart := end.Add(30 * time.Minute)
	newEnd := end.Add(time.Hour)
	body, err := json.Marshal(map[string]any{"operation_id": uuid.NewString(), "lease_id": lease, "occurred_at": newStart})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	correction := make(chan error, 1)
	clock := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		<-release
		correction <- svc.CorrectTimeEntry(ctx, actor, entry.ID, authorization.TimeEntryCorrection{ClockOut: &newEnd})
	}()
	go func() {
		<-release
		r := httptest.NewRequest("POST", "/time-entries/clock-in", strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		api.ServeHTTP(w, r)
		clock <- w
	}()
	close(release)
	corrErr, response := <-correction, <-clock
	if corrErr == nil {
		if response.Code != 409 || !strings.Contains(response.Body.String(), "time_overlap") {
			t.Fatalf("correction won but new clock=%d %s", response.Code, response.Body.String())
		}
		if client.TimeEntry.Query().CountX(ctx) != 1 || !client.TimeEntry.GetX(ctx, entry.ID).ClockOut.Equal(newEnd) {
			t.Fatal("winning correction not retained exactly")
		}
	} else {
		var conflict *authorization.ClockConflict
		if !errors.As(corrErr, &conflict) || conflict.Code != "time_overlap" || response.Code != 200 {
			t.Fatalf("clock winner response=%d %s correction=%v", response.Code, response.Body.String(), corrErr)
		}
		if client.TimeEntry.Query().CountX(ctx) != 2 || !client.TimeEntry.GetX(ctx, entry.ID).ClockOut.Equal(end) {
			t.Fatal("rejected correction partially changed entry")
		}
	}
	var afterGPS string
	if err := pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(g) ORDER BY event)::text FROM time_clock_gps g WHERE entry_id=$1`, entry.ID).Scan(&afterGPS); err != nil {
		t.Fatal(err)
	}
	if afterGPS != originalGPS {
		t.Fatalf("correction changed recorded GPS: before=%s after=%s", originalGPS, afterGPS)
	}
}
