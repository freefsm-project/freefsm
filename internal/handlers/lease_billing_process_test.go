package handlers

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLeaseBillingRestartAndDrain(t *testing.T) {
	if os.Getenv("FREEFSM_LEASE_WORKER_CHILD") == "1" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		cfg, err := pgxpool.ParseConfig(os.Getenv("FREEFSM_LEASE_CHILD_DSN"))
		historyCheck(t, err)
		cfg.ConnConfig.RuntimeParams["application_name"] = os.Getenv("FREEFSM_LEASE_CHILD_APP")
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		historyCheck(t, err)
		defer pool.Close()
		control, err := instancecontrol.New(os.Getenv("FREEFSM_LEASE_CHILD_STATE"))
		historyCheck(t, err)
		err = authorization.New(pool).RunLeaseBilling(ctx, control, func() error { return nil })
		if !errors.Is(err, context.Canceled) {
			historyCheck(t, err)
		}
		return
	}
	pool, client, web, sessions, company, admin := permissionHTTPFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-01'::date $$`)
	historyCheck(t, err)
	lease, _ := populateFinalizedLease(t, pool, client, web, sessionCookie(t, ctx, sessions, admin), company)
	var pending int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT id FROM lease_billing_obligations WHERE lease_id=$1 AND kind='monthly'`, lease).Scan(&pending))
	_, err = pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lease_dependency_today(company bigint) RETURNS date LANGUAGE sql STABLE AS $$ SELECT '2024-01-02'::date $$; CREATE FUNCTION process_billing_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='lease.invoice.generate' THEN PERFORM pg_advisory_xact_lock(670301); END IF; RETURN NEW; END $$; CREATE TRIGGER process_billing_barrier AFTER INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION process_billing_barrier()`)
	historyCheck(t, err)
	barrier, err := pool.Begin(ctx)
	historyCheck(t, err)
	defer barrier.Rollback(context.Background())
	var holder int
	historyCheck(t, barrier.QueryRow(ctx, `SELECT pg_backend_pid() FROM pg_advisory_xact_lock(670301)`).Scan(&holder))
	control, err := instancecontrol.New(t.TempDir())
	historyCheck(t, err)
	dsn, err := dsnWithSearchPath(os.Getenv("FREEFSM_TEST_DATABASE_URL"), pool.Config().ConnConfig.RuntimeParams["search_path"])
	historyCheck(t, err)
	start := func() (*exec.Cmd, func()) {
		childCtx, kill := context.WithCancel(ctx)
		cmd := exec.CommandContext(childCtx, os.Args[0], "-test.run=^TestLeaseBillingRestartAndDrain$", "-test.v")
		cmd.Env = append(os.Environ(), "FREEFSM_LEASE_WORKER_CHILD=1", "FREEFSM_LEASE_CHILD_DSN="+dsn, "FREEFSM_LEASE_CHILD_APP="+pool.Config().ConnConfig.RuntimeParams["application_name"], "FREEFSM_LEASE_CHILD_STATE="+control.StateDir())
		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		historyCheck(t, cmd.Start())
		done := make(chan struct{})
		var waitErr error
		go func() { waitErr = cmd.Wait(); close(done) }()
		t.Cleanup(func() { kill(); <-done })
		return cmd, func() {
			historyCheck(t, cmd.Process.Signal(syscall.SIGTERM))
			<-done
			t.Logf("worker process: %s", output.String())
			historyCheck(t, waitErr)
		}
	}
	_, stop := start()
	commercialWaiters(t, ctx, pool, holder, 1)
	var ids []int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT array_agg(id ORDER BY id) FROM lease_billing_obligations`).Scan(&ids))
	historyEqual(t, "pending committed across process boundary", len(ids), 2)
	operationRelease, err := control.OperationLock()
	historyCheck(t, err)
	defer operationRelease()
	historyCheck(t, control.KeepClosed())
	if _, release, err := control.Enter(ctx); err == nil {
		release()
		t.Fatal("maintenance admitted new work")
	}
	// Observe the actual cross-process shared admission lock, not a sleep.
	lock, err := os.OpenFile(filepath.Join(control.StateDir(), "admission.lock"), os.O_RDWR, 0600)
	historyCheck(t, err)
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		t.Fatal("active generation did not hold admission")
	}
	drained := make(chan func(), 1)
	drainErr := make(chan error, 1)
	go func() {
		release, err := control.Maintenance(ctx)
		if err != nil {
			drainErr <- err
			return
		}
		drained <- release
	}()
	stop()
	var release func()
	select {
	case release = <-drained:
	case err := <-drainErr:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var generated, invoices int
	historyCheck(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM lease_invoice_generations),(SELECT count(*) FROM invoices)`).Scan(&generated, &invoices))
	historyEqual(t, "canceled generation has no success marker", generated, 1)
	historyEqual(t, "canceled generation has no Invoice", invoices, 1)
	historyCheck(t, control.ClearMaintenance())
	release()
	operationRelease()
	historyCheck(t, barrier.Commit(ctx))
	_, err = pool.Exec(ctx, `DROP TRIGGER process_billing_barrier ON authorization_audit; CREATE FUNCTION process_billing_committed() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_notify('lease67_process_committed',NEW.obligation_id::text); RETURN NEW; END $$; CREATE TRIGGER process_billing_committed AFTER INSERT ON lease_invoice_generations FOR EACH ROW EXECUTE FUNCTION process_billing_committed()`)
	historyCheck(t, err)
	observer, err := pool.Acquire(ctx)
	historyCheck(t, err)
	defer observer.Release()
	_, err = observer.Exec(ctx, `LISTEN lease67_process_committed`)
	historyCheck(t, err)
	_, stopRestart := start()
	_, err = observer.Conn().WaitForNotification(ctx)
	historyCheck(t, err)
	stopRestart()
	var restoredID int64
	historyCheck(t, pool.QueryRow(ctx, `SELECT obligation_id FROM lease_invoice_generations WHERE obligation_id=$1`, pending).Scan(&restoredID))
	historyEqual(t, "new process resumes exact pending identity", restoredID, pending)
	historyCheck(t, pool.QueryRow(ctx, `SELECT count(*) FROM invoices`).Scan(&invoices))
	historyEqual(t, "one resumed Invoice", invoices, 2)
}
