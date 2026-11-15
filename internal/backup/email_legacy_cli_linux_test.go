package backup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sys/unix"
)

func TestEmailLegacyJournalCLIRecoveryImport(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "freefsm")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/freefsm")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, output)
	}
	for _, mode := range []string{"migrate", "startup"} {
		for _, disabled := range []bool{false, true} {
			name := mode + "/enabled"
			if disabled {
				name = mode + "/disabled"
			}
			t.Run(name, func(t *testing.T) {
				m, db := preflightFixture(t, true)
				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				defer cancel()
				// Materialize the actual pre-policy schema, not a synthetic journal.
				if _, err := db.Pool.Exec(ctx, `DROP TABLE restore_email_policy; DROP TABLE restore_email_policy_import; ALTER TABLE online_save_actions DROP COLUMN execution_review; DELETE FROM schema_migrations WHERE name='127_restore_email_policy'; CREATE TABLE legacy_import_sentinel(value text); INSERT INTO legacy_import_sentinel VALUES('archive')`); err != nil {
					t.Fatal(err)
				}
				m.cfg.Version, m.cfg.Commit, m.cfg.BuildKind = "v1.2.3", "abcdef1234567", BuildRelease
				file := filepath.Join(m.cfg.UploadDir, "legacy-sentinel")
				if err := os.WriteFile(file, []byte("archive"), 0600); err != nil {
					t.Fatal(err)
				}
				archive := createCrashArchive(t, m)
				if _, err := db.Pool.Exec(ctx, `UPDATE legacy_import_sentinel SET value='destination'`); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte("destination"), 0600); err != nil {
					t.Fatal(err)
				}
				if disabled {
					if err := m.cfg.Control.DisableEmail(); err != nil {
						t.Fatal(err)
					}
				}
				runCrashChild(t, m, archive, "restore", "protections-applied")
				recoveryEvidence(t, m)
				assertCrashMaintenance(t, m)
				cfg, err := pgx.ParseConfig(m.cfg.DSN)
				if err != nil {
					t.Fatal(err)
				}
				adminConfig, err := pgx.ParseConfig(os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL"))
				if err != nil {
					t.Fatal(err)
				}
				adminConfig.Database = cfg.Database
				admin, err := pgx.ConnectConfig(ctx, adminConfig)
				if err != nil {
					t.Fatal(err)
				}
				defer admin.Close(context.Background())
				// A fixture-admin-owned event trigger survives replacement of public
				// and pauses the actual migration only after legacy recovery has run.
				// The application role remains unprivileged.
				if _, err := admin.Exec(ctx, `CREATE SCHEMA qualification_control;
				CREATE FUNCTION qualification_control.pause_policy_migration() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN
				IF EXISTS(SELECT 1 FROM pg_event_trigger_ddl_commands() WHERE object_identity='public.restore_email_policy_import') THEN PERFORM pg_advisory_xact_lock(941096); END IF;
				END $$;
				CREATE EVENT TRIGGER pause_policy_migration ON ddl_command_end WHEN TAG IN ('CREATE TABLE') EXECUTE FUNCTION qualification_control.pause_policy_migration();
				SELECT pg_advisory_lock(941096)`); err != nil {
					t.Fatal(err)
				}
				defer admin.Exec(context.Background(), `SELECT pg_advisory_unlock(941096)`)
				var env []string
				for _, value := range os.Environ() {
					if !strings.HasPrefix(value, "FREEFSM_") && !strings.HasPrefix(value, "PG") {
						env = append(env, value)
					}
				}
				env = append(env, "FREEFSM_DB_HOST="+cfg.Host, "FREEFSM_DB_PORT="+strconv.Itoa(int(cfg.Port)), "FREEFSM_DB_NAME="+cfg.Database, "FREEFSM_DB_USER="+cfg.User, "FREEFSM_DB_PASSWORD="+cfg.Password,
					"FREEFSM_SESSION_SECRET=disposable-session", "FREEFSM_SETUP_TOKEN=disposable-setup", "FREEFSM_ADDR=127.0.0.1:0", "FREEFSM_UPLOAD_DIR="+m.cfg.UploadDir, "FREEFSM_STATE_DIR="+m.cfg.StateDir)
				args := []string{}
				if mode == "migrate" {
					args = append(args, "-migrate")
				}
				cmd := exec.CommandContext(ctx, binary, args...)
				cmd.Env = env
				logPath := filepath.Join(t.TempDir(), "startup.log")
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
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				waited := false
				defer func() {
					if !waited {
						_ = unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
						<-done
					}
				}()
				for {
					var blocked bool
					if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE objid=941096 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&blocked); err != nil {
						t.Fatal(err)
					}
					if blocked {
						break
					}
					select {
					case err := <-done:
						waited = true
						data, _ := os.ReadFile(logPath)
						t.Fatalf("CLI exited before migration barrier: %v %s", err, data)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
				var recovered string
				if err := admin.QueryRow(ctx, `SELECT value FROM public.legacy_import_sentinel`).Scan(&recovered); err != nil || recovered != "destination" {
					t.Fatalf("migration preceded legacy rollback: %s %v", recovered, err)
				}
				if unlock, err := m.cfg.Control.OperationLock(); !errors.Is(err, instancecontrol.ErrOperationBusy) {
					if unlock != nil {
						unlock()
					}
					t.Fatalf("recovery-to-import lost operation reservation: %v", err)
				}
				if _, leave, err := m.cfg.Control.Enter(ctx); !errors.Is(err, instancecontrol.ErrMaintenance) {
					if leave != nil {
						leave()
					}
					t.Fatalf("legacy recovery prematurely admitted traffic before import: %v", err)
				}
				if _, err := admin.Exec(ctx, `SELECT pg_advisory_unlock(941096)`); err != nil {
					t.Fatal(err)
				}
				if mode == "startup" {
					for {
						data, err := os.ReadFile(logPath)
						if err != nil {
							t.Fatal(err)
						}
						if strings.Contains(string(data), "listening") {
							break
						}
						select {
						case err := <-done:
							waited = true
							t.Fatalf("startup exited: %v %s", err, data)
						case <-ctx.Done():
							t.Fatalf("startup: %s %v", data, ctx.Err())
						case <-time.After(10 * time.Millisecond):
						}
					}
					if err := unix.PidfdSendSignal(pidfd, unix.SIGTERM, nil, 0); err != nil {
						t.Fatal(err)
					}
				}
				err = <-done
				waited = true
				data, readErr := os.ReadFile(logPath)
				if err != nil || readErr != nil {
					t.Fatalf("CLI: %v %v %s", err, readErr, data)
				}
				t.Logf("actual legacy recovery CLI %q: %s", cmd.Args, data)
				db.Pool.Reset()
				var sentinel string
				if err := db.Pool.QueryRow(ctx, `SELECT value FROM legacy_import_sentinel`).Scan(&sentinel); err != nil || sentinel != "destination" {
					t.Fatalf("rollback generation: %s %v", sentinel, err)
				}
				contents, err := os.ReadFile(file)
				if err != nil || string(contents) != "destination" {
					t.Fatalf("file generation: %q %v", contents, err)
				}
				policy, err := emailpolicy.Read(ctx, db.Pool, false)
				if err != nil || policy.Enabled == disabled || policy.LastEnableOperation != nil {
					t.Fatalf("post-recovery legacy import: %+v %v", policy, err)
				}
				if _, err := os.Stat(m.journalPath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("journal remains: %v", err)
				}
				if _, leave, err := m.cfg.Control.Enter(ctx); err != nil {
					t.Fatal(err)
				} else {
					leave()
				}
			})
		}
	}
}
