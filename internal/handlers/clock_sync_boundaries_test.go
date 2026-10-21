package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func grantClockFixture(t *testing.T, pool *pgxpool.Pool, role int64) {
	t.Helper()
	for _, permission := range []string{"time.view", "time.clock_in", "time.clock_out", "time.correct"} {
		if _, err := pool.Exec(context.Background(), `INSERT INTO authorization_grants(role_id,permission,scope) VALUES($1,$2,'O')`, role, permission); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClockSyncOfflinePolicyKeepsIssuedBoundsAndOnlineCloseoutHTTP(t *testing.T) {
	f, api, token, _, role, _ := syncFixture(t, 0)
	grantClockFixture(t, f.pool, role)
	ctx := context.Background()
	issue := func(operation string, want int) authorization.ClockLeaseReceipt {
		t.Helper()
		request := httptest.NewRequest("POST", "/time-entries/lease", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Operation-ID", operation)
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("Clock lease=%d want%d: %s", response.Code, want, response.Body.String())
		}
		var decoded struct {
			Data authorization.ClockLeaseReceipt `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded.Data
	}
	initialID := uuid.NewString()
	initial := issue(initialID, 200)
	if initial.ClockLease == nil || initial.ExpiresAt.Sub(initial.IssuedAt) != 24*time.Hour {
		t.Fatalf("default window: %+v", initial)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE company_settings SET offline_hours=72 WHERE company_id=$1`, f.company); err != nil {
		t.Fatal(err)
	}
	replay := issue(initialID, 200)
	if replay.ID != initial.ID || !replay.ExpiresAt.Equal(initial.ExpiresAt) {
		t.Fatal("renewal rewrote an issued lease")
	}
	long := issue(uuid.NewString(), 200)
	if long.ClockLease == nil || long.ExpiresAt.Sub(long.IssuedAt) != 72*time.Hour {
		t.Fatalf("configured72-hour window ignored: %+v", long)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE company_settings SET offline_hours=0 WHERE company_id=$1`, f.company); err != nil {
		t.Fatal(err)
	}
	denied := uuid.NewString()
	issue(denied, 403)
	if receipt := clockSyncRequest(t, api, token, "GET", "/clock-lease-operations/"+denied, nil, 200); receipt["code"] != "forbidden" {
		t.Fatalf("disabled issuance not immutable: %v", receipt)
	}
	if again := issue(initialID, 200); again.ID != initial.ID || !again.ExpiresAt.Equal(initial.ExpiresAt) {
		t.Fatal("disabled policy changed original receipt")
	}
	offline := uuid.NewString()
	clockSyncRequest(t, api, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": offline, "lease_id": initial.ID, "occurred_at": initial.IssuedAt}, 403)
	if receipt := clockSyncRequest(t, api, token, "GET", "/time-operations/"+offline, nil, 200); receipt["code"] != "forbidden" {
		t.Fatalf("disabled offline effect not rejected: %v", receipt)
	}
	online := clockSyncRequest(t, api, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString()}, 200)
	clockSyncRequest(t, api, token, "POST", "/time-entries/clock-out", map[string]any{"operation_id": uuid.NewString(), "entry_id": online["entry_id"]}, 200)
}

func TestClockBatchShapeRejectsBeforeEarlierEffectsHTTP(t *testing.T) {
	for _, command := range []string{`{"operation_id":""}`, `{"entry_ref":""}`, `{"entry_ref":null}`, `{"entry_id":1,"entry_ref":"1"}`, `{"user_id":123}`, `{"gps":null}`} {
		t.Run(command, func(t *testing.T) {
			f, api, token, _, role, _ := syncFixture(t, 0)
			grantClockFixture(t, f.pool, role)
			first := uuid.NewString()
			body := fmt.Sprintf(`{"operations":[{"operation_id":%q,"type":"clock.in","depends_on":[],"clock":{}},{"operation_id":%q,"type":"clock.in","depends_on":[],"clock":%s}]}`, first, uuid.NewString(), command)
			response := batchRequest(api, token, body)
			if response.Code != 400 {
				t.Fatalf("malformed later member=%d: %s", response.Code, response.Body.String())
			}
			clockSyncRequest(t, api, token, "GET", "/time-operations/"+first, nil, 404)
			snapshot := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)
			if len(snapshot["records"].([]any)) != 0 {
				t.Fatalf("earlier clock committed despite invalid envelope: %v", snapshot)
			}
		})
	}
}

func TestClockSyncReopenedManualExactCloseoutHTTP(t *testing.T) {
	f, api, token, uid, role, _ := syncFixture(t, 0)
	grantClockFixture(t, f.pool, role)
	ctx := context.Background()
	start := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	entry := f.client.TimeEntry.Create().SetCompanyID(f.company).SetUserID(uid).SetIsManual(true).SetClockIn(start).SetClockOut(start.Add(time.Minute)).SaveX(ctx)
	if err := authorization.New(f.pool).CorrectTimeEntry(ctx, authorization.Actor{UserID: uid, Channel: "web"}, entry.ID, authorization.TimeEntryCorrection{Reason: "Reviewed reopening", Reopen: true}); err != nil {
		t.Fatal(err)
	}
	before := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)
	if len(before["records"].([]any)) != 1 {
		t.Fatalf("existing active entry absent from own clock state: %v", before)
	}
	clockSyncRequest(t, api, token, "POST", "/time-entries/clock-out", map[string]any{"operation_id": uuid.NewString(), "entry_id": entry.ID}, 200)
	after := clockSyncRequest(t, api, token, "GET", "/sync/clocks?cursor="+before["completion_cursor"].(string), nil, 200)
	rows := after["records"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["state"] != "closed" {
		t.Fatalf("closed original entry disappeared: %v", after)
	}
}

func TestClockBatchAuditRollbackSameIDHTTP(t *testing.T) {
	f, api, token, _, role, _ := syncFixture(t, 0)
	grantClockFixture(t, f.pool, role)
	ctx := context.Background()
	operation := uuid.NewString()
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION reject83_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='time.clock_in' THEN RAISE EXCEPTION 'injected clock audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject83_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject83_audit()`); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"operations": []any{map[string]any{"operation_id": operation, "type": "clock.in", "depends_on": []string{}, "clock": map[string]any{}}}}
	failed := clockSyncRequest(t, api, token, "POST", "/sync/operations", body, 200)["results"].([]any)[0].(map[string]any)
	if failed["receipt"] != nil || failed["error"].(map[string]any)["status"] != float64(503) {
		t.Fatalf("audit failure became terminal: %v", failed)
	}
	clockSyncRequest(t, api, token, "GET", "/time-operations/"+operation, nil, 404)
	if rows := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)["records"].([]any); len(rows) != 0 {
		t.Fatalf("audit failure left clock/history: %v", rows)
	}
	if _, err := f.pool.Exec(ctx, `DROP TRIGGER reject83_audit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	accepted := clockSyncRequest(t, api, token, "POST", "/sync/operations", body, 200)["results"].([]any)[0].(map[string]any)["receipt"].(map[string]any)
	if accepted["code"] != "accepted" {
		t.Fatalf("same-ID retry: %v", accepted)
	}
	rows := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)["records"].([]any)
	if len(rows) != 1 || len(rows[0].(map[string]any)["history"].([]any)) != 1 {
		t.Fatalf("retry duplicated time/history: %v", rows)
	}
}

func TestClockBatchActualCloseoutRaceNeverClosesNewerHTTP(t *testing.T) {
	f, api, token, _, role, _ := syncFixture(t, 0)
	grantClockFixture(t, f.pool, role)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	first := clockSyncRequest(t, api, token, "POST", "/time-entries/clock-in", map[string]any{"operation_id": uuid.NewString()}, 200)
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION pause83_close() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.clock_out IS NULL AND NEW.clock_out IS NOT NULL THEN PERFORM pg_advisory_xact_lock(830083); END IF; RETURN NEW; END $$; CREATE TRIGGER pause83_close BEFORE UPDATE OF clock_out ON time_entries FOR EACH ROW EXECUTE FUNCTION pause83_close()`); err != nil {
		t.Fatal(err)
	}
	holder, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Rollback(context.WithoutCancel(ctx))
	var blocker int32
	if err = holder.QueryRow(ctx, `SELECT pg_backend_pid() FROM (SELECT pg_advisory_xact_lock(830083)) held`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	outID, newID := uuid.NewString(), uuid.NewString()
	outBody, _ := json.Marshal(map[string]any{"operations": []any{map[string]any{"operation_id": outID, "type": "clock.out", "depends_on": []string{}, "clock": map[string]any{"entry_id": first["entry_id"]}}}})
	inBody, _ := json.Marshal(map[string]any{"operations": []any{map[string]any{"operation_id": newID, "type": "clock.in", "depends_on": []string{}, "clock": map[string]any{}}}})
	closed := make(chan *httptest.ResponseRecorder, 1)
	opened := make(chan *httptest.ResponseRecorder, 1)
	go func() { closed <- batchRequest(api, token, string(outBody)) }()
	writer := waitClockCloseMutation(t, ctx, f.pool, blocker)
	assertForeignAuthorityExcluded(t, ctx, f.pool, writer)
	go func() { opened <- batchRequest(api, token, string(inBody)) }()
	waitAuthorityRequest(t, ctx, f.pool, writer, authorityWriterStatement)
	if err = holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan *httptest.ResponseRecorder{closed, opened} {
		select {
		case response := <-done:
			var decoded struct {
				Data struct {
					Results []struct{ Receipt struct{ Code string } }
				}
			}
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || len(decoded.Data.Results) != 1 || decoded.Data.Results[0].Receipt.Code != "accepted" {
				t.Fatalf("serialized effect failed: %d %s", response.Code, response.Body.String())
			}
		case <-ctx.Done():
			t.Fatal("clock race did not complete")
		}
	}
	replay := batchRequest(api, token, string(outBody))
	if replay.Code != 200 {
		t.Fatalf("original out replay: %d", replay.Code)
	}
	snapshot := clockSyncRequest(t, api, token, "GET", "/sync/clocks", nil, 200)
	active, oldClosed := 0, false
	for _, raw := range snapshot["records"].([]any) {
		entry := raw.(map[string]any)
		if entry["state"] == "active" {
			active++
			if entry["entry_id"] == fmt.Sprint(first["entry_id"]) {
				t.Fatal("old entry remained active")
			}
		}
		if entry["entry_id"] == fmt.Sprint(first["entry_id"]) && entry["state"] == "closed" && len(entry["history"].([]any)) == 2 {
			oldClosed = true
		}
	}
	if active != 1 || !oldClosed {
		t.Fatalf("race or replay changed newer clock/history: %v", snapshot)
	}
}

func waitClockCloseMutation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker int32) int32 {
	t.Helper()
	application := pool.Config().ConnConfig.RuntimeParams["application_name"]
	if application == "" {
		t.Fatal("fixture application required")
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var pids []int32
		if err := pool.QueryRow(ctx, `SELECT coalesce(array_agg(a.pid),'{}'::integer[]) FROM pg_stat_activity a WHERE a.datname=current_database() AND a.application_name=$2 AND $1=ANY(pg_blocking_pids(a.pid)) AND a.query='UPDATE time_entries SET clock_out=$2,updated_at=$3 WHERE id=$1' AND a.wait_event='advisory' AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.classid=0 AND l.objid=830083 AND NOT l.granted) AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.classid=0 AND l.objid=190019 AND l.granted)`, blocker, application).Scan(&pids); err != nil {
			t.Fatal(err)
		}
		if len(pids) == 1 {
			t.Logf("actual clock-out UPDATE backend=%d fixture=%s blocker=%d; current-authority fence held", pids[0], application, blocker)
			return pids[0]
		}
		if len(pids) > 1 {
			t.Fatalf("ambiguous clock mutation: %v", pids)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("clock-out never reached its actual UPDATE boundary")
	return 0
}
