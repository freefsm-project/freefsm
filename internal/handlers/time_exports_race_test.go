package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/exports"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Witness the actual domain DML, not admission or a request launch. Both the
// held test barrier and production authority lock must belong to this backend.
func timeExportDMLWaiter(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker int32, key int64, statement string) int32 {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var pid int32
		err := pool.QueryRow(ctx, `SELECT a.pid FROM pg_stat_activity a WHERE a.datname=current_database() AND a.application_name=$1 AND $2=ANY(pg_blocking_pids(a.pid)) AND a.wait_event='advisory' AND a.query LIKE $3
		 AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND l.classid=0 AND l.objid=$4 AND l.objsubid=1 AND NOT l.granted)
		 AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND l.classid=0 AND l.objid=190019 AND l.objsubid=1 AND l.granted)`, pool.Config().ConnConfig.RuntimeParams["application_name"], blocker, statement+"%", key).Scan(&pid)
		if err == nil {
			t.Logf("actual DML fixture=%s backend=%d blocker=%d barrier=%d statement=%s", pool.Config().ConnConfig.RuntimeParams["application_name"], pid, blocker, key, statement)
			return pid
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("actual fixture DML did not reach barrier holding authority")
	return 0
}

func TestTimeExportActualMutationSnapshotOrders(t *testing.T) {
	for _, first := range []string{"export", "correction"} {
		t.Run(first, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cookie := sessionCookie(t, ctx, sessions, admin)
			start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
			entry := client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("Original snapshot").SaveX(ctx)
			path := masterPost(t, router, cookie, "/time-entries/export", url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}, 303)
			holder, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(context.Background())
			if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(100101)`); err != nil {
				t.Fatal(err)
			}
			var blocker int32
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			sql := `CREATE FUNCTION hold_time_export_dml() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100101);RETURN NEW; END $$; `
			statement := "INSERT INTO export_records"
			if first == "export" {
				sql += `CREATE TRIGGER hold_time_export_dml BEFORE INSERT ON export_records FOR EACH ROW WHEN(NEW.ordinal=1) EXECUTE FUNCTION hold_time_export_dml()`
			} else {
				sql += `CREATE TRIGGER hold_time_export_dml BEFORE UPDATE ON time_entries FOR EACH ROW WHEN(NEW.notes IS DISTINCT FROM OLD.notes) EXECUTE FUNCTION hold_time_export_dml()`
				statement = "UPDATE time_entries"
			}
			if _, err := pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			exported, corrected := make(chan error, 1), make(chan error, 1)
			export := func() { _, err := exports.Product(pool).ProcessOne(ctx); exported <- err }
			correct := func() {
				end := start.Add(2 * time.Hour)
				notes := "Corrected snapshot"
				corrected <- authorization.New(pool).CorrectTimeEntry(ctx, authorization.Actor{UserID: admin}, entry.ID, authorization.TimeEntryCorrection{Reason: "Actual race correction", ClockOut: &end, Notes: &notes})
			}
			if first == "export" {
				go export()
			} else {
				go correct()
			}
			writer := timeExportDMLWaiter(t, ctx, pool, blocker, 100101, statement)
			assertForeignAuthorityExcluded(t, ctx, pool, writer)
			if first == "export" {
				go correct()
				waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
			} else {
				go export()
				waitAuthorityRequest(t, ctx, pool, writer, authoritySnapshotStatement)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-exported; err != nil {
				t.Fatal(err)
			}
			if err := <-corrected; err != nil {
				t.Fatal(err)
			}
			rows := timeExportCSV(t, requestBody(t, router, cookie, "GET", path+"/download", 200))
			if len(rows) != 1 {
				t.Fatalf("snapshot rows: %v", rows)
			}
			wantNote, wantDuration := "Original snapshot", "3600000000"
			if first == "correction" {
				wantNote, wantDuration = "Corrected snapshot", "7200000000"
			}
			if rows[0]["notes"] != wantNote || rows[0]["duration_microseconds"] != wantDuration {
				t.Fatalf("mixed snapshot: %v", rows)
			}
			var notes string
			var seconds float64
			if err := pool.QueryRow(ctx, `SELECT notes,extract(epoch FROM clock_out-clock_in) FROM time_entries WHERE id=$1`, entry.ID).Scan(&notes, &seconds); err != nil {
				t.Fatal(err)
			}
			if notes != "Corrected snapshot" || seconds != 7200 {
				t.Fatalf("lost mutation: %s %v", notes, seconds)
			}
		})
	}
}

func TestTimeExportActualGrantRevocationBoundaries(t *testing.T) {
	for _, phase := range []string{"accept", "generate", "download"} {
		t.Run(phase, func(t *testing.T) {
			pool, client, router, sessions, company, admin := permissionHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			auth := authorization.New(pool)
			actor := authorization.Actor{UserID: admin}
			reader := client.User.Create().SetCompanyID(company).SetEmail("race-export@test").SetName("Race exporter").SetRole("tech").SetPasswordHash("hash").SaveX(ctx)
			role, err := auth.SaveRole(ctx, actor, 0, "Revoked export", []authorization.Grant{{Permission: "time.view", Scope: authorization.All}, {Permission: "time.export", Scope: authorization.All}})
			if err != nil {
				t.Fatal(err)
			}
			if err := auth.AssignUser(ctx, actor, reader.ID, role); err != nil {
				t.Fatal(err)
			}
			cookie := sessionCookie(t, ctx, sessions, reader.ID)
			start := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
			client.TimeEntry.Create().SetCompanyID(company).SetUserID(admin).SetClockIn(start).SetClockOut(start.Add(time.Hour)).SetNotes("Must not disclose").SaveX(ctx)
			values := url.Values{"operation_id": {newOperationKey()}, "scope": {"A"}}
			path := ""
			if phase != "accept" {
				path = masterPost(t, router, cookie, "/time-entries/export", values, 303)
			}
			if phase == "download" {
				if _, err := exports.Product(pool).ProcessOne(ctx); err != nil {
					t.Fatal(err)
				}
			}
			var revision int64
			if err := pool.QueryRow(ctx, `SELECT revision FROM authorization_roles WHERE id=$1`, role).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			holder, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer holder.Rollback(context.Background())
			if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(100102)`); err != nil {
				t.Fatal(err)
			}
			var blocker int32
			if err := holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `CREATE FUNCTION hold_time_export_revoke() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(100102);RETURN OLD;END $$; CREATE TRIGGER hold_time_export_revoke BEFORE DELETE ON authorization_grants FOR EACH ROW WHEN(OLD.permission='time.export') EXECUTE FUNCTION hold_time_export_revoke()`); err != nil {
				t.Fatal(err)
			}
			revoked := make(chan error, 1)
			go func() {
				_, err := auth.SaveRole(ctx, actor, role, "Revoked export", []authorization.Grant{{Permission: "time.view", Scope: authorization.All}}, revision)
				revoked <- err
			}()
			writer := timeExportDMLWaiter(t, ctx, pool, blocker, 100102, "DELETE FROM authorization_grants")
			assertForeignAuthorityExcluded(t, ctx, pool, writer)
			done := make(chan error, 1)
			if phase == "generate" {
				go func() { _, err := exports.Product(pool).ProcessOne(ctx); done <- err }()
				waitAuthorityRequest(t, ctx, pool, writer, authoritySnapshotStatement)
			} else {
				go func() {
					method, target, body := "GET", path+"/download", ""
					want := 410
					if phase == "accept" {
						method, target, body, want = "POST", "/time-entries/export", values.Encode(), 403
					}
					r := httptest.NewRequest(method, target, strings.NewReader(body))
					r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					r.AddCookie(cookie)
					w := httptest.NewRecorder()
					router.ServeHTTP(w, r)
					if w.Code != want || strings.Contains(w.Body.String(), "Must not disclose") {
						done <- fmt.Errorf("response %d: %s", w.Code, w.Body.String())
						return
					}
					done <- nil
				}()
				waitAuthorityRequest(t, ctx, pool, writer, authorityWriterStatement)
			}
			if err := holder.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-revoked; err != nil {
				t.Fatal(err)
			}
			result := <-done
			if phase == "generate" {
				if result == nil {
					t.Fatal("revoked generation succeeded")
				}
				assertContains(t, requestBody(t, router, cookie, "GET", path, 200), "Failed")
			} else if result != nil {
				t.Fatal(result)
			}
			var accepted int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM export_jobs WHERE operation_id=$1`, values.Get("operation_id")).Scan(&accepted); err != nil {
				t.Fatal(err)
			}
			if phase == "accept" && accepted != 0 {
				t.Fatal("revoked acceptance persisted")
			}
		})
	}
}
