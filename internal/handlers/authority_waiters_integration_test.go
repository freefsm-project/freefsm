package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	authorityWriterStatement   = "SELECT pg_advisory_xact_lock(190019)"
	authoritySnapshotStatement = "SELECT pg_advisory_lock(190019)"
)

// Identify the actual fixture request behind this fence. Database-wide advisory
// wait counts include unrelated tests, even when their schemas are isolated.
func authorityWaiterPIDs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker int32, statement string) []int32 {
	t.Helper()
	application := pool.Config().ConnConfig.RuntimeParams["application_name"]
	if application == "" {
		t.Fatal("authority contention requires fixture application identity")
	}
	rows, err := pool.Query(ctx, `SELECT a.pid FROM pg_stat_activity a
		WHERE a.datname=current_database() AND a.application_name=$2
		AND $1=ANY(pg_blocking_pids(a.pid)) AND a.wait_event='advisory' AND a.query=$3
		AND EXISTS (SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory'
		AND l.database=(SELECT oid FROM pg_database WHERE datname=current_database())
		AND l.classid=0 AND l.objid=190019 AND l.objsubid=1 AND NOT l.granted)
		ORDER BY a.pid`, blocker, application, statement)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var pids []int32
	for rows.Next() {
		var pid int32
		if err := rows.Scan(&pid); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return pids
}

func waitAuthorityRequest(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker int32, statement string) int32 {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		pids := authorityWaiterPIDs(t, ctx, pool, blocker, statement)
		if len(pids) == 1 {
			t.Logf("fixture=%s backend=%d blocked by=%d statement=%s", pool.Config().ConnConfig.RuntimeParams["application_name"], pids[0], blocker, statement)
			return pids[0]
		}
		if len(pids) > 1 {
			t.Fatalf("ambiguous request contenders: %v", pids)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("fixture request did not reach authority fence: blocker=%d statement=%s", blocker, statement)
	return 0
}

func assertForeignAuthorityExcluded(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker int32) {
	t.Helper()
	foreignAuthorityWaiter(t, ctx, pool, blocker)
	for _, statement := range []string{authorityWriterStatement, authoritySnapshotStatement} {
		if pids := authorityWaiterPIDs(t, ctx, pool, blocker, statement); len(pids) != 0 {
			t.Fatalf("foreign waiter falsely establishes readiness before request launch: %v", pids)
		}
	}
}

// Hold a real foreign fixture backend on the same database-wide authority fence.
// Observe its exact wait edge before testing readiness, not its launch time.
func foreignAuthorityWaiter(t *testing.T, ctx context.Context, pool *pgxpool.Pool, blocker int32) {
	t.Helper()
	config := pool.Config().ConnConfig.Copy()
	config.RuntimeParams["application_name"] += "_foreign"
	foreign, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	done := make(chan struct{})
	pid := int32(foreign.PgConn().PID())
	go func() {
		defer close(done)
		_, _ = foreign.Exec(waitCtx, `SELECT pg_advisory_xact_lock(190019)`)
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = foreign.Close(closeCtx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("foreign authority waiter did not exit")
		}
	})
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		var waiting bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND $2=ANY(pg_blocking_pids(pid)) AND wait_event='advisory')`, pid, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			t.Logf("controlled foreign backend=%d blocked by=%d", pid, blocker)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("foreign fixture did not reach held authority fence")
}
