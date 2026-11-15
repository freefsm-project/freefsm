package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"
)

// These start the actual application executable against a schema predating the
// policy migration. Marker permission failures are OS failures on owned paths.
func TestEmailStartupLegacyImportFaults(t *testing.T) {
	for _, mode := range []string{"migrate", "startup"} {
		for _, marker := range []string{"absent", "disabled", "unreadable", "nonregular", "missing-modern"} {
			t.Run(mode+"/"+marker, func(t *testing.T) {
				binary, env, db := installationFixture(t)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				pool, err := pgxpool.New(ctx, db.Config().ConnString())
				if err != nil {
					t.Fatal(err)
				}
				defer pool.Close()
				legacy := fstest.MapFS{}
				entries, err := fs.ReadDir(database.MigrationFS(), ".")
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if !strings.HasSuffix(entry.Name(), ".up.sql") || entry.Name() >= "127_" {
						continue
					}
					data, err := fs.ReadFile(database.MigrationFS(), entry.Name())
					if err != nil {
						t.Fatal(err)
					}
					legacy[entry.Name()] = &fstest.MapFile{Data: data}
				}
				if err := (&database.DB{Pool: pool}).Migrate(ctx, legacy); err != nil {
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
				path := filepath.Join(stateDir, "email-disabled")
				switch marker {
				case "disabled", "unreadable":
					if err := os.WriteFile(path, []byte("retained legacy bytes\n"), 0600); err != nil {
						t.Fatal(err)
					}
					if marker == "unreadable" {
						if err := os.Chmod(path, 0); err != nil {
							t.Fatal(err)
						}
						defer os.Chmod(path, 0600)
						if _, err := os.ReadFile(path); !errors.Is(err, os.ErrPermission) {
							t.Fatalf("OS unreadability precondition: %v", err)
						}
					}
				case "nonregular":
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				case "missing-modern":
					if output, err := runInstallationCLI(binary, env, "", "-migrate"); err != nil {
						t.Fatalf("modern initialize: %v %s", err, output)
					}
					if _, err := pool.Exec(ctx, `DELETE FROM restore_email_policy`); err != nil {
						t.Fatal(err)
					}
				}
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
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				waited := false
				defer func() {
					if !waited {
						_ = unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
						<-done
					}
				}()
				negative := marker == "unreadable" || marker == "nonregular" || marker == "missing-modern"
				if mode == "startup" && !negative {
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
							t.Fatal(ctx.Err())
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
				if readErr != nil {
					t.Fatal(readErr)
				}
				t.Logf("actual executable argv=%q mode=%s marker=%s exit=%v output=%s", cmd.Args, mode, marker, err, data)
				if negative {
					if err == nil || strings.Contains(string(data), "listening") {
						t.Fatalf("fault admitted traffic: %v %s", err, data)
					}
					if _, release, err := control.Enter(ctx); !errors.Is(err, instancecontrol.ErrMaintenance) {
						if release != nil {
							release()
						}
						t.Fatalf("failed startup admission: %v", err)
					}
					if _, err := emailpolicy.Read(ctx, pool, false); err == nil {
						t.Fatal("fault imported usable policy")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					policy, err := emailpolicy.Read(ctx, pool, false)
					if err != nil || policy.Enabled != (marker == "absent") || policy.LastEnableOperation != nil {
						t.Fatalf("legacy import: %+v %v", policy, err)
					}
					if marker == "disabled" {
						b, err := os.ReadFile(path)
						if err != nil || string(b) != "retained legacy bytes\n" {
							t.Fatalf("legacy evidence changed: %q %v", b, err)
						}
					}
				}
			})
		}
	}
}
