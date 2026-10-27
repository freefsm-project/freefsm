package handlers

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// exportSnapshotWriter injects only the deliberately out-of-band source changes
// that challenge an export's MVCC snapshot. The real HTTP/worker paths retain the
// normal application role and all production triggers. Never promote that role
// or disable a table's guard globally just to make the fixture edit independent.
func exportSnapshotWriter(t *testing.T, ctx context.Context, pool *pgxpool.Pool) func(int, func(pgx.Tx) error) {
	t.Helper()
	var schema, runtimeUser, replicationRole string
	var privileged bool
	if err := pool.QueryRow(ctx, `SELECT current_schema(),current_user,current_setting('session_replication_role'),rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR has_parameter_privilege(current_user,'session_replication_role','SET') FROM pg_roles WHERE rolname=current_user`).Scan(&schema, &runtimeUser, &replicationRole, &privileged); err != nil {
		t.Fatal(err)
	}
	if privileged || replicationRole != "origin" || !strings.HasPrefix(schema, "permission_http_") {
		t.Fatal("export snapshot fixture requires an unprivileged origin-mode runtime in its isolated permission_http schema")
	}
	dsn := os.Getenv("FREEFSM_INSTALLATION_TEST_ADMIN_URL")
	if dsn == "" {
		t.Fatal("set FREEFSM_INSTALLATION_TEST_ADMIN_URL to a separate administrator on the disposable test cluster for out-of-band export snapshot edits")
	}
	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid export fixture administrator configuration")
	}
	runtimeConfig := pool.Config().ConnConfig
	if adminConfig.Host != runtimeConfig.Host || adminConfig.Port != runtimeConfig.Port {
		t.Fatal("export fixture administrator must name the same disposable host and port as the runtime")
	}
	adminConfig.Database = runtimeConfig.Database
	adminConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	adminConfig.RuntimeParams["application_name"] = schema + "_mvcc_fixture"
	admin, err := pgx.ConnectConfig(ctx, adminConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	var adminUser, adminSchema, adminMode string
	if err := admin.QueryRow(ctx, `SELECT current_user,current_schema(),current_setting('session_replication_role')`).Scan(&adminUser, &adminSchema, &adminMode); err != nil {
		t.Fatal(err)
	}
	if adminUser == runtimeUser || adminSchema != schema || adminMode != "origin" {
		t.Fatal("export fixture administrator must be a distinct origin-mode role scoped to this test's schema")
	}
	return func(pausePID int, mutate func(pgx.Tx) error) {
		t.Helper()
		var exporterPID int
		if err := admin.QueryRow(ctx, `SELECT a.pid FROM pg_stat_activity a WHERE a.datname=current_database() AND a.application_name=$2 AND $1=ANY(pg_blocking_pids(a.pid)) AND a.wait_event='advisory' AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.classid=0 AND l.objid=190019 AND l.granted)`, pausePID, runtimeConfig.RuntimeParams["application_name"]).Scan(&exporterPID); err != nil {
			t.Fatalf("identify paused export holding the authority fence: %v", err)
		}
		// A separate connection avoids depending on the runtime pool's size.
		// WHERE false changes no data, but still exercises the statement guard.
		probeConfig := runtimeConfig.Copy()
		probeConfig.RuntimeParams["application_name"] = schema + "_guard_probe"
		probe, err := pgx.ConnectConfig(ctx, probeConfig)
		if err != nil {
			t.Fatal(err)
		}
		probeCtx, cancel := context.WithCancel(ctx)
		var probeErr error
		finished := make(chan struct{})
		probePID := int(probe.PgConn().PID())
		go func() {
			_, probeErr = probe.Exec(probeCtx, `UPDATE customers SET display_name=display_name WHERE false`)
			close(finished)
		}()
		defer func() { cancel(); <-finished; _ = probe.Close(context.Background()) }()
		for {
			select {
			case <-finished:
				t.Fatalf("runtime Customer statement escaped the export authority fence: %v", probeErr)
			default:
			}
			var waiting bool
			if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a JOIN pg_locks l ON l.pid=a.pid WHERE a.pid=$1 AND a.usename=$3 AND a.application_name=$4 AND a.wait_event='advisory' AND $2=ANY(pg_blocking_pids(a.pid)) AND l.locktype='advisory' AND l.classid=0 AND l.objid=190019 AND NOT l.granted)`, probePID, exporterPID, runtimeUser, probeConfig.RuntimeParams["application_name"]).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("runtime Customer statement did not reach the expected export fence")
			case <-time.After(10 * time.Millisecond):
			}
		}
		cancel()
		<-finished
		if probeErr == nil {
			t.Fatal("canceled fenced probe unexpectedly committed")
		}
		// This is a fixture-only physical mutation, not an application operation.
		// Its backend-local mode is reset by commit/rollback. No GRANT/ALTER ROLE
		// or globally disabled trigger is involved; export authorization is intact.
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			t.Fatal(err)
		}
		if err := mutate(tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var stillPaused bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND $2=ANY(pg_blocking_pids(pid)) AND wait_event='advisory'),current_setting('session_replication_role')`, exporterPID, pausePID).Scan(&stillPaused, &adminMode); err != nil {
			t.Fatal(err)
		}
		if !stillPaused || adminMode != "origin" {
			t.Fatal("source edit did not commit independently while export remained paused, or fixture mode leaked")
		}
		if err := pool.QueryRow(ctx, `SELECT current_setting('session_replication_role')`).Scan(&replicationRole); err != nil {
			t.Fatal(err)
		}
		if replicationRole != "origin" {
			t.Fatal("fixture changed runtime trigger mode")
		}
		t.Logf("observed export snapshot: pause=%d exporter=%d guarded-runtime-probe=%d runtime=%s fixture-admin=%s; independent source edit committed while export stayed paused", pausePID, exporterPID, probePID, runtimeUser, adminUser)
	}
}
