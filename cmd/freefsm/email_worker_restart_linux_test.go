package main

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestEmailApplicationWorkerReconstruction(t *testing.T) {
	binary, env, db := installationFixture(t)
	if output, err := runInstallationCLI(binary, env, "", "-migrate"); err != nil {
		t.Fatalf("initialize: %v %s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := db.Exec(ctx, `UPDATE restore_email_policy SET enabled=false,revision=revision+1;
	INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 941,id,'restart@test','Restart','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin',1 FROM companies LIMIT 1;
	INSERT INTO password_recovery_requests(email,base_url,user_id,credential_revision) SELECT email,'http://owned.invalid/reset',id,credential_revision FROM users WHERE id=941`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE company_settings SET smtp_host='127.0.0.1',smtp_port=$1,smtp_from='sender@example.test'`, listener.Addr().(*net.TCPAddr).Port); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"disabled-first-process", "disabled-reconstructed-process", "unavailable-process"} {
		t.Run(state, func(t *testing.T) {
			if state == "unavailable-process" {
				if _, err := db.Exec(ctx, `DELETE FROM restore_email_policy`); err != nil {
					t.Fatal(err)
				}
			}
			logPath := filepath.Join(t.TempDir(), "application.log")
			log, err := os.Create(logPath)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			cmd := exec.CommandContext(ctx, binary)
			cmd.Env, cmd.Dir = env, filepath.Dir(binary)
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
			if state != "unavailable-process" {
				for {
					data, err := os.ReadFile(logPath)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(data), "listening") && strings.Contains(string(data), "recovery dispatch deferred") {
						break
					}
					select {
					case err := <-done:
						waited = true
						t.Fatalf("worker startup exited: %v %s", err, data)
					case <-ctx.Done():
						t.Fatalf("worker did not attempt dispatch: %s", data)
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
			t.Logf("fresh application pid=%d pidfd=%d argv=%q state=%s exit=%v logs=%s", cmd.Process.Pid, pidfd, cmd.Args, state, err, data)
			if state == "unavailable-process" {
				if err == nil || strings.Contains(string(data), "listening") {
					t.Fatal("unavailable policy admitted full application")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var pending, tokens int
			if err := db.QueryRow(ctx, `SELECT (SELECT count(*) FROM password_recovery_requests WHERE completed_at IS NULL AND claimed_until IS NULL),(SELECT count(*) FROM password_reset_tokens)`).Scan(&pending, &tokens); err != nil || pending != 1 || tokens != 0 {
				t.Fatalf("reconstructed worker changed queued lifecycle: pending=%d tokens=%d err=%v", pending, tokens, err)
			}
			if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			conn, err := listener.Accept()
			if conn != nil {
				conn.Close()
				t.Fatal("reconstructed worker reached sink")
			}
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("sink observation: %v", err)
			}
		})
	}
}
