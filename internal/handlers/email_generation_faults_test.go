package handlers

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/delivery"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/services"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Construct both workers and warm their separate pool before the real encrypted
// restore replaces the public schema. The returned probe runs those same workers
// against the restored generation, rather than constructing a post-restore cache.
func prepareEmailGenerationWorkers(t *testing.T, ctx context.Context, dst *historyBackupInstance) func() {
	t.Helper()
	pool, err := pgxpool.New(ctx, dst.managerConfig.DSN)
	historyCheck(t, err)
	t.Cleanup(pool.Close)
	var oldPID uint32
	historyCheck(t, pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&oldPID))
	prior, err := emailpolicy.Read(ctx, pool, false)
	historyCheck(t, err)
	if !prior.Enabled {
		t.Fatal("preexisting worker must start in enabled generation")
	}
	control, err := instancecontrol.New(dst.control.StateDir())
	historyCheck(t, err)
	var resets, refreshes atomic.Int32
	cfg := dst.managerConfig
	cfg.Control = control
	cfg.ResetConnections = func() { pool.Reset(); resets.Add(1) }
	manager, err := backup.New(cfg)
	historyCheck(t, err)
	t.Cleanup(manager.Shutdown)
	refresh := func() error {
		refreshes.Add(1)
		return manager.RefreshConnections()
	}
	type observation struct {
		state     emailpolicy.State
		err       error
		pid       uint32
		refreshes int32
		resets    int32
	}
	observed := make(chan observation, 16)
	control.SetEmailPolicy(func(ctx context.Context) (bool, error) {
		p, err := emailpolicy.Read(ctx, pool, false)
		var pid uint32
		if err == nil {
			err = pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid)
		}
		observed <- observation{p, err, pid, refreshes.Load(), resets.Load()}
		return p.Enabled, err
	})
	outbox := delivery.New(pool, "")
	outbox.SetInstanceControl(control)
	outbox.SetBeforeWork(refresh)
	email := services.NewControlledEmailService(services.NewCompanySettingsService(dst.client), control)
	recovery := services.NewPasswordResetService(dst.client, pool)
	worker := &delivery.Worker{Service: outbox, Sender: delivery.NewSMTPSender(email), PollInterval: time.Hour}
	parent, release, err := control.EnterEmailAfterRefresh(ctx, refresh)
	historyCheck(t, err)
	defer release()
	<-observed
	if worked, err := outbox.ProcessOne(parent, worker.Sender, nil); err != nil || worked {
		t.Fatalf("nested empty destination worker: worked=%v err=%v", worked, err)
	}
	cancelled, cancelNested := context.WithCancel(parent)
	cancelNested()
	if _, err := outbox.ProcessOne(cancelled, worker.Sender, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("nested Delivery cancellation: %v", err)
	}
	if _, err := recovery.DispatchOne(cancelled, email); !errors.Is(err, context.Canceled) {
		t.Fatalf("nested recovery cancellation: %v", err)
	}
	drained := make(chan error, 1)
	go func() {
		lease, err := control.BeginMaintenance(ctx)
		if err == nil {
			lease.Close()
		}
		drained <- err
	}()
	for {
		_, leave, err := control.Enter(ctx)
		if errors.Is(err, instancecontrol.ErrMaintenance) {
			break
		}
		historyCheck(t, err)
		leave()
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	select {
	case err := <-drained:
		t.Fatalf("worker cancellation released the live outer lease: %v", err)
	default:
	}
	release()
	select {
	case err := <-drained:
		historyCheck(t, err)
	case <-ctx.Done():
		t.Fatal("nested worker lease did not drain")
	}
	historyCheck(t, manager.Recover(ctx))
	historyCheck(t, manager.RefreshConnections())
	historyCheck(t, pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&oldPID))
	baselineResets := resets.Load()
	return func() {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		historyCheck(t, err)
		defer listener.Close()
		_, err = dst.pool.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1`, listener.Addr().(*net.TCPAddr).Port)
		historyCheck(t, err)
		for _, kind := range []string{"delivery", "recovery"} {
			workCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			if kind == "delivery" {
				go func() { done <- worker.Run(workCtx) }()
			} else {
				go func() { done <- recovery.RunDispatch(workCtx, email, refresh) }()
			}
			var got observation
			select {
			case got = <-observed:
			case <-ctx.Done():
				cancel()
				t.Fatal(ctx.Err())
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("worker cancellation: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("worker did not drain")
			}
			if got.err != nil || got.state.Enabled || got.state.Epoch == prior.Epoch || got.pid == oldPID || got.refreshes == 0 || got.resets <= baselineResets {
				t.Fatalf("%s used stale generation: prior=%+v oldPID=%d got=%+v", kind, prior, oldPID, got)
			}
			t.Logf("pre-created %s worker after actual schema replacement: oldPID=%d currentPID=%d oldEpoch=%s currentEpoch=%s refreshes=%d resets=%d disabled=true", kind, oldPID, got.pid, prior.Epoch, got.state.Epoch, got.refreshes, got.resets)
		}
		// A synchronous accept deadline independently observes zero TCP handoffs.
		historyCheck(t, listener.(*net.TCPListener).SetDeadline(time.Now().Add(50*time.Millisecond)))
		conn, err := listener.Accept()
		if conn != nil {
			conn.Close()
			t.Fatal("disabled restored worker reached owned sink")
		}
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Fatalf("sink observation: %v", err)
		}
		lease, err := control.BeginMaintenance(ctx)
		historyCheck(t, err)
		lease.Close()
		// Reopen through the actual Manager after cancellation has drained leases.
		historyCheck(t, manager.Recover(ctx))
	}
}
