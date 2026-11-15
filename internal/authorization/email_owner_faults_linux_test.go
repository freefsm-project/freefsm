package authorization_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"
)

type emailOwnerConfig struct {
	DSN, Schema, Boundary, Ready string
	Action                       saveaction.Action
}

type emailCommitPauseConn struct {
	net.Conn
	ready string
	seen  []byte
}

func (c *emailCommitPauseConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.seen = append(c.seen, p[:n]...)
	if bytes.Contains(c.seen, []byte("COMMIT\x00")) {
		if err := os.WriteFile(c.ready, []byte("server COMMIT received; caller unacknowledged\n"), 0600); err != nil {
			return 0, err
		}
		// Parent owns the pidfd and kills this actual command owner here.
		select {}
	}
	if len(c.seen) > 16 {
		c.seen = append([]byte(nil), c.seen[len(c.seen)-16:]...)
	}
	return n, err
}

func TestEmailEnableOwnerProcess(t *testing.T) {
	path := os.Getenv("FREEFSM_EMAIL_OWNER_CONFIG")
	if path == "" {
		return // Parent-only helper; no qualification is claimed by this root.
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var config emailOwnerConfig
	if err := json.Unmarshal(b, &config); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(config.DSN)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = config.Schema
	if config.Boundary != "before-commit" {
		cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			return &emailCommitPauseConn{Conn: conn, ready: config.Ready}, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	actor := authorization.Actor{UserID: 941, SessionHash: "owner-session", Channel: "web"}
	err = authorization.New(pool).EnableEmail(saveaction.WithAction(ctx, config.Action), actor)
	t.Fatalf("owner unexpectedly returned before kill: %v", err)
}

func TestEmailEnableOwnerDeath(t *testing.T) {
	for _, boundary := range []string{"before-commit", "after-commit", "after-no-change"} {
		t.Run(boundary, func(t *testing.T) {
			pool, authority := fixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 941,id,'owner@test','Owner','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin',1 FROM companies LIMIT 1;
			INSERT INTO sessions(user_id,token_hash,kind,expires_at) VALUES(941,'owner-session','web',clock_timestamp()+interval '1 hour')`); err != nil {
				t.Fatal(err)
			}
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if err := emailpolicy.Import(ctx, tx, func() (bool, error) { return boundary != "after-no-change", nil }); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			policy, err := emailpolicy.Read(ctx, pool, false)
			if err != nil {
				t.Fatal(err)
			}
			a := saveaction.Action{UserID: 941, ID: uuid.NewString(), ExecutionToken: uuid.NewString(), Path: authorization.EmailEnablePath, Revision: policy.Review(), IntentHash: "owner:" + policy.Review(), Atomic: true, NoChange: true}
			store := saveaction.Store{Pool: pool}
			if ok, err := store.AdmitExecution(ctx, a, a.IntentHash, "POST", a.Path, "/settings/mail"); err != nil || !ok {
				t.Fatalf("admission: %v %v", ok, err)
			}
			if boundary == "before-commit" {
				if _, err := pool.Exec(ctx, `CREATE FUNCTION pause_owner_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(941098); RETURN NEW; END $$;
				CREATE TRIGGER pause_owner_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION pause_owner_audit()`); err != nil {
					t.Fatal(err)
				}
			}
			blocker, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Release()
			if _, err := blocker.Exec(ctx, `SELECT pg_advisory_lock(941098)`); err != nil {
				t.Fatal(err)
			}
			defer blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(941098)`)
			dir := t.TempDir()
			config := emailOwnerConfig{DSN: os.Getenv("FREEFSM_TEST_DATABASE_URL"), Schema: pool.Config().ConnConfig.RuntimeParams["search_path"], Boundary: boundary, Ready: filepath.Join(dir, "ready"), Action: a}
			b, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(dir, "config.json")
			if err := os.WriteFile(configPath, b, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEmailEnableOwnerProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "FREEFSM_EMAIL_OWNER_CONFIG="+configPath)
			pidfd := -1
			cmd.SysProcAttr = &syscall.SysProcAttr{PidFD: &pidfd}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
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
			proc := filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid))
			identity, err := os.ReadFile(filepath.Join(proc, "stat"))
			if err != nil {
				t.Fatal(err)
			}
			executable, err := os.Readlink(filepath.Join(proc, "exe"))
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("owned command argv=%q uid=%d pidfd=%d executable=%s stat=%s action=%s token=%s", cmd.Args, os.Getuid(), pidfd, executable, identity, a.ID, a.ExecutionToken)
			for {
				ready := false
				if boundary == "before-commit" {
					if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE objid=941098 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&ready); err != nil {
						t.Fatal(err)
					}
				} else {
					_, err := os.Stat(config.Ready)
					ready = err == nil
				}
				if ready {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("owner did not reach boundary", ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			if err := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			waited = true
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatalf("owner death: %v %s", err, output.String())
			}
			if _, err := blocker.Exec(ctx, `SELECT pg_advisory_unlock(941098)`); err != nil {
				t.Fatal(err)
			}
			actor := authorization.Actor{UserID: 941, SessionHash: "owner-session", Channel: "web"}
			want, wantEffects := "saved", 1
			if boundary == "before-commit" {
				want, wantEffects = "rejected", 0
			} else if boundary == "after-no-change" {
				want, wantEffects = "completed", 0
			}
			for range 2 {
				if outcome, err := authority.ReconcileEmailEnable(ctx, actor, a); err != nil || outcome != want {
					t.Fatalf("owner-death reconciliation: %s %v, want %s", outcome, err, want)
				}
			}
			var audit, effects int
			if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1::text),(SELECT count(*) FROM online_save_effects WHERE operation_id=$1::uuid)`, a.ID).Scan(&audit, &effects); err != nil {
				t.Fatal(err)
			}
			after, err := emailpolicy.Read(ctx, pool, false)
			if err != nil || after.Enabled != (boundary != "before-commit") || audit != wantEffects || effects != wantEffects {
				t.Fatalf("atomicity: policy=%+v audit=%d effects=%d err=%v", after, audit, effects, err)
			}
			if receipt, err := store.Receipt(ctx, a); err != nil || receipt.Disposition != want || receipt.CommittedSteps != wantEffects {
				t.Fatalf("receipt: %+v %v", receipt, err)
			}
			if err := authority.EnableEmail(saveaction.WithAction(ctx, a), actor); !errors.Is(err, saveaction.ErrExecutionFenced) {
				t.Fatalf("dead original replay: %v", err)
			}
		})
	}
}
