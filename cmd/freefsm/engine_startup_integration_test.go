package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func TestEngineStartupAndExplicitMigrationKeepExclusiveOwnership(t *testing.T) {
	binary, env, db := installationFixture(t)
	if output, err := runInstallationCLI(binary, env, "", "-migrate"); err != nil {
		t.Fatalf("initialize: %v\n%s", err, output)
	}
	var stateDir string
	for _, value := range env {
		if strings.HasPrefix(value, "FREEFSM_STATE_DIR=") {
			stateDir = strings.TrimPrefix(value, "FREEFSM_STATE_DIR=")
		}
	}
	control, err := instancecontrol.New(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"migrate", "startup"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			tx, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err = tx.Exec(ctx, "LOCK TABLE schema_migrations IN ACCESS EXCLUSIVE MODE"); err != nil {
				t.Fatal(err)
			}
			args := []string{}
			if mode == "migrate" {
				args = append(args, "-migrate")
			}
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Env = env
			logPath := filepath.Join(t.TempDir(), "process.log")
			log, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd.Stdout, cmd.Stderr = log, log
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				var waiting bool
				if _, err = tx.Exec(ctx, "SELECT pg_stat_clear_snapshot()"); err != nil {
					t.Fatal(err)
				}
				err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND wait_event_type='Lock' AND query LIKE '%schema_migrations%')`).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("executable did not reach initialization barrier")
				case <-tick.C:
				}
			}
			if unlock, err := control.OperationLock(); !errors.Is(err, instancecontrol.ErrOperationBusy) {
				if unlock != nil {
					unlock()
				}
				t.Fatalf("initialization lost operation ownership: %v", err)
			}
			if _, release, err := control.Enter(ctx); !errors.Is(err, instancecontrol.ErrMaintenance) {
				if release != nil {
					release()
				}
				t.Fatalf("initialization admitted work: %v", err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			// Both modes must release the operation only after initialization.
			for {
				unlock, err := control.OperationLock()
				if err == nil {
					unlock()
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("initialization never released operation")
				case <-tick.C:
				}
			}
			if mode == "startup" {
				for {
					b, err := os.ReadFile(logPath)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(b), "listening") {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatalf("startup did not serve after initialization: %s", b)
					case <-tick.C:
					}
				}
				if err = cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			if err = cmd.Wait(); err != nil {
				t.Fatalf("executable completion: %v", err)
			}
		})
	}
}
