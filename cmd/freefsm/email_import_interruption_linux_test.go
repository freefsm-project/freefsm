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

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"
)

func TestEmailStartupInterruptedImportReservation(t *testing.T) {
	for _, mode := range []string{"migrate", "startup"} {
		for _, boundary := range []string{"before-commit", "during-commit"} {
			t.Run(mode+"/"+boundary, func(t *testing.T) {
				binary, env, db := installationFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				pool, err := pgxpool.New(ctx, db.Config().ConnString())
				if err != nil {
					t.Fatal(err)
				}
				defer pool.Close()
				if err := (&database.DB{Pool: pool}).Migrate(ctx, database.MigrationFS()); err != nil {
					t.Fatal(err)
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
				if err := control.DisableEmail(); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(ctx, `CREATE FUNCTION pause_policy_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(941097); RETURN NEW; END $$`); err != nil {
					t.Fatal(err)
				}
				trigger := `CREATE TRIGGER pause_policy_import BEFORE UPDATE ON restore_email_policy_import FOR EACH ROW EXECUTE FUNCTION pause_policy_import()`
				if boundary == "during-commit" {
					trigger = `CREATE CONSTRAINT TRIGGER pause_policy_import AFTER UPDATE ON restore_email_policy_import DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION pause_policy_import()`
				}
				if _, err := db.Exec(ctx, trigger); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(ctx, `SELECT pg_advisory_lock(941097)`); err != nil {
					t.Fatal(err)
				}
				defer db.Exec(context.Background(), `SELECT pg_advisory_unlock(941097)`)
				args := []string{}
				if mode == "migrate" {
					args = append(args, "-migrate")
				}
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Env, cmd.Dir = env, filepath.Dir(binary)
				logPath := filepath.Join(t.TempDir(), "application.log")
				log, err := os.Create(logPath)
				if err != nil {
					t.Fatal(err)
				}
				defer log.Close()
				cmd.Stdout, cmd.Stderr = log, log
				pidfd := -1
				cmd.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				defer unix.Close(pidfd)
				waited := false
				defer func() {
					if !waited {
						_ = unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
						_ = cmd.Wait()
					}
				}()
				for {
					var blocked bool
					if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE objid=941097 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&blocked); err != nil {
						t.Fatal(err)
					}
					if blocked {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
				if unlock, err := control.OperationLock(); !errors.Is(err, instancecontrol.ErrOperationBusy) {
					if unlock != nil {
						unlock()
					}
					t.Fatalf("import lost exclusive operation reservation: %v", err)
				}
				if _, release, err := control.Enter(ctx); !errors.Is(err, instancecontrol.ErrMaintenance) {
					if release != nil {
						release()
					}
					t.Fatalf("import admitted premature traffic: %v", err)
				}
				if _, err := emailpolicy.Read(ctx, pool, false); err == nil {
					t.Fatal("uncommitted import visible")
				}
				if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil {
					t.Fatal(err)
				}
				err = cmd.Wait()
				waited = true
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
					t.Fatalf("actual import-owner death: %v", err)
				}
				if _, err := db.Exec(ctx, `SELECT pg_advisory_unlock(941097)`); err != nil {
					t.Fatal(err)
				}
				// DDL waits for the interrupted transaction's locks, establishing a
				// terminal outcome before inspecting the atomic import/completion pair.
				if _, err := db.Exec(ctx, `DROP TRIGGER pause_policy_import ON restore_email_policy_import`); err != nil {
					t.Fatal(err)
				}
				var complete bool
				var policies int
				if err := db.QueryRow(ctx, `SELECT complete,(SELECT count(*) FROM restore_email_policy) FROM restore_email_policy_import`).Scan(&complete, &policies); err != nil || (complete && policies != 1) || (!complete && policies != 0) {
					t.Fatalf("partial import: complete=%v policies=%d err=%v", complete, policies, err)
				}
				if boundary == "before-commit" && complete {
					t.Fatal("owner died before COMMIT but import committed")
				}
				t.Logf("actual %s import owner SIGKILL at %s: complete=%v policies=%d; continuous reservation denied traffic", mode, boundary, complete, policies)
				if output, err := runInstallationCLI(binary, env, "", "-migrate"); err != nil {
					t.Fatalf("restart: %v %s", err, output)
				}
				policy, err := emailpolicy.Read(ctx, pool, false)
				if err != nil || policy.Enabled || policy.LastEnableOperation != nil {
					t.Fatalf("reconciled import: %+v %v", policy, err)
				}
			})
		}
	}
}
