package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func clockSyncRequest(t *testing.T, api http.Handler, token, method, path string, body any, want int) map[string]any {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, strings.NewReader(string(b)))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	api.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	var result struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Data
}

func TestClockSyncRetainedOwnSnapshotAndDeltaHTTP(t *testing.T) {
	f, api, token, uid, role, jobs := syncFixture(t, 1)
	ctx := context.Background()
	for _, permission := range []string{"time.view", "time.clock_in", "time.clock_out", "time.gps.capture"} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,'O')`, role, permission); err != nil {
			t.Fatal(err)
		}
	}
	accepted := clockSyncRequest(t, api, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString(), "job_id": jobs[0]}, 200)
	first := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)
	rows := first["records"].([]any)
	if len(rows) != 1 {
		t.Fatalf("own clocks: %v", first)
	}
	entry := rows[0].(map[string]any)
	if entry["entry_id"] != fmt.Sprint(accepted["entry_id"]) || entry["subject_id"] != fmt.Sprint(uid) || entry["state"] != "active" {
		t.Fatalf("identity/state: %v", entry)
	}
	if _, ok := entry["gps"]; ok {
		t.Fatal("GPS disclosed without independent View")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE job_id=$1 AND user_id=$2;`, jobs[0], uid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE jobs SET deleted_at=clock_timestamp(),job_type='PRIVATE RENAMED' WHERE id=$1`, jobs[0]); err != nil {
		t.Fatal(err)
	}
	clockSyncRequest(t, api, token, "POST", "/time-entries/clock-out", map[string]any{"operation_id": uuid.NewString(), "entry_id": accepted["entry_id"]}, 200)
	delta := clockSyncRequest(t, api, token, "GET", "/sync/clocks?cursor="+first["completion_cursor"].(string), nil, 200)
	rows = delta["records"].([]any)
	if len(rows) != 1 {
		t.Fatalf("missing close-out delta: %v", delta)
	}
	entry = rows[0].(map[string]any)
	job := entry["job"].(map[string]any)
	if entry["state"] != "closed" || job["title"] != "Job 0" || job["id"] != fmt.Sprint(jobs[0]) || len(job) != 2 || strings.Contains(fmt.Sprint(delta), "PRIVATE") {
		t.Fatalf("unsafe retained projection: %v", delta)
	}
	if len(entry["history"].([]any)) != 2 {
		t.Fatalf("clock history lost: %v", entry)
	}
}

func TestClockBatchDependenciesReplayAndNoNewerCloseHTTP(t *testing.T) {
	f, api, token, _, role, _ := syncFixture(t, 0)
	ctx := context.Background()
	for _, permission := range []string{"time.view", "time.clock_in", "time.clock_out"} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,'O')`, role, permission); err != nil {
			t.Fatal(err)
		}
	}
	first, blocked, independent := uuid.NewString(), uuid.NewString(), uuid.NewString()
	operations := []any{
		map[string]any{"operation_id": first, "type": "clock.in", "depends_on": []string{uuid.NewString()}, "clock": map[string]any{}},
		map[string]any{"operation_id": blocked, "type": "clock.in", "depends_on": []string{first}, "clock": map[string]any{}},
		map[string]any{"operation_id": independent, "type": "clock.in", "depends_on": []string{}, "clock": map[string]any{}},
	}
	body := map[string]any{"operations": operations}
	result := clockSyncRequest(t, api, token, "POST", "/sync/operations", body, 200)
	rows := result["results"].([]any)
	for i, code := range []string{"invalid_dependency", "failed_dependency", "accepted"} {
		if rows[i].(map[string]any)["receipt"].(map[string]any)["code"] != code {
			t.Fatalf("batch outcomes: %v", result)
		}
	}
	replayed := clockSyncRequest(t, api, token, "POST", "/sync/operations", body, 200)
	for i, raw := range replayed["results"].([]any) {
		if fmt.Sprint(raw.(map[string]any)["receipt"]) != fmt.Sprint(rows[i].(map[string]any)["receipt"]) {
			t.Fatalf("immutable replay: %v vs %v", replayed, result)
		}
	}
	entry := rows[2].(map[string]any)["receipt"].(map[string]any)["entry_id"]
	out := map[string]any{"operations": []any{map[string]any{"operation_id": uuid.NewString(), "type": "clock.out", "depends_on": []string{independent}, "clock": map[string]any{"entry_id": entry}}}}
	closed := clockSyncRequest(t, api, token, "POST", "/sync/operations", out, 200)
	if closed["results"].([]any)[0].(map[string]any)["receipt"].(map[string]any)["code"] != "accepted" {
		t.Fatalf("exact out: %v", closed)
	}
	newer := clockSyncRequest(t, api, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString()}, 200)
	clockSyncRequest(t, api, token, "POST", "/sync/operations", out, 200)
	snapshot := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)
	for _, raw := range snapshot["records"].([]any) {
		row := raw.(map[string]any)
		if row["entry_id"] == fmt.Sprint(newer["entry_id"]) && row["state"] != "active" {
			t.Fatalf("replay closed newer entry: %v", row)
		}
	}
}

func TestClockSyncGPSAuthorityLossInvalidatesMaterializedPagesHTTP(t *testing.T) {
	f, api, token, uid, role, jobs := syncFixture(t, 1)
	ctx := context.Background()
	for permission, scope := range map[string]string{"time.view": "A", "time.clock_in": "O", "time.clock_out": "O", "time.gps.capture": "O", "time.gps.view": "J"} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,$3)`, role, permission, scope); err != nil {
			t.Fatal(err)
		}
	}
	entry := clockSyncRequest(t, api, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString(), "job_id": jobs[0], "gps": map[string]any{"latitude": 51.5, "longitude": -0.12, "captured_at": time.Now().UTC().Truncate(time.Microsecond)}}, 200)
	clockSyncRequest(t, api, token, "POST", "/time-entries/clock-out", map[string]any{"operation_id": uuid.NewString(), "entry_id": entry["entry_id"]}, 200)
	clockSyncRequest(t, api, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString(), "job_id": jobs[0]}, 200)
	first := clockSyncRequest(t, api, token, "GET", "/sync/clocks?page_size=1", nil, 200)
	complete := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)
	if len(first["records"].([]any)[0].(map[string]any)["gps"].([]any)) != 1 {
		t.Fatal("authorized GPS missing")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM job_assignments WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	clockSyncRequest(t, api, token, "GET", "/sync/clocks?cursor="+complete["completion_cursor"].(string), nil, 409)
	clockSyncRequest(t, api, token, "GET", "/sync/clocks?page_size=1&page="+first["next_page"].(string), nil, 409)
	fresh := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)
	for _, raw := range fresh["records"].([]any) {
		if _, ok := raw.(map[string]any)["gps"]; ok {
			t.Fatalf("lost assigned GPS disclosed: %v", raw)
		}
	}
}
