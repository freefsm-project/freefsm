package authorization_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRestoreEmailPolicy(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		name := "enabled"
		if disabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			pool, _ := fixture(t)
			ctx := context.Background()
			importPolicy := func(commit bool, observe func() (bool, error)) error {
				tx, err := pool.Begin(ctx)
				if err != nil {
					return err
				}
				defer tx.Rollback(ctx)
				if err = emailpolicy.Import(ctx, tx, observe); err != nil {
					return err
				}
				if commit {
					return tx.Commit(ctx)
				}
				return nil
			}
			observe := func() (bool, error) { return disabled, nil }
			if err := importPolicy(false, observe); err != nil {
				t.Fatal(err)
			}
			if _, err := emailpolicy.Read(ctx, pool, false); err == nil {
				t.Fatal("uncommitted import visible")
			}
			if err := importPolicy(true, func() (bool, error) { return false, errors.New("ambiguous marker") }); err == nil {
				t.Fatal("ambiguous observation accepted")
			}
			if err := importPolicy(true, observe); err != nil {
				t.Fatal(err)
			}
			state, err := emailpolicy.Read(ctx, pool, false)
			if err != nil || state.Enabled == disabled || state.LastEnableOperation != nil {
				t.Fatalf("import: %+v %v", state, err)
			}
			never := func() (bool, error) { t.Error("modern import consulted legacy marker"); return false, nil }
			if err := importPolicy(true, never); err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE restore_email_policy SET epoch=gen_random_uuid()`); err != nil {
				t.Fatal(err)
			}
			if err := importPolicy(true, never); err == nil {
				t.Fatal("corrupt epoch accepted")
			}
			if _, err := pool.Exec(ctx, `DELETE FROM restore_email_policy`); err != nil {
				t.Fatal(err)
			}
			if err := importPolicy(true, never); err == nil {
				t.Fatal("missing modern policy reimported")
			}
		})
	}
}

func TestSealedEmailPolicy(t *testing.T) {
	pool, authority := fixture(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 941,id,'policy@test','Policy','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin',1 FROM companies LIMIT 1;
	INSERT INTO sessions(user_id,token_hash,kind,expires_at) VALUES(941,'policy-session','web',clock_timestamp()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := emailpolicy.Import(ctx, tx, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	actor := authorization.Actor{UserID: 941, SessionHash: "policy-session", Channel: "web"}
	store := saveaction.Store{Pool: pool}
	admit := func(t *testing.T) saveaction.Action {
		t.Helper()
		p, err := emailpolicy.Read(ctx, pool, false)
		if err != nil {
			t.Fatal(err)
		}
		a := saveaction.Action{UserID: 941, ID: uuid.NewString(), ExecutionToken: uuid.NewString(), Path: authorization.EmailEnablePath, Revision: p.Review(), IntentHash: "enable:" + p.Review(), Atomic: true, NoChange: true}
		if ok, err := store.AdmitExecution(ctx, a, a.IntentHash, "POST", a.Path, "/settings/mail"); err != nil || !ok {
			t.Fatalf("admit %v %v", ok, err)
		}
		return a
	}
	execute := func(a saveaction.Action) error { return authority.EnableEmail(saveaction.WithAction(ctx, a), actor) }
	snapshot := func(a saveaction.Action) string {
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT to_jsonb(p) FROM restore_email_policy p),(SELECT to_jsonb(a) FROM online_save_actions a WHERE operation_id=$1),(SELECT count(*) FROM online_save_effects WHERE operation_id=$1),(SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1::text))::text`, a.ID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	t.Run("required-boundary", func(t *testing.T) {
		if err := authority.EnableEmail(ctx, actor); err == nil {
			t.Fatal("absent action accepted")
		}
		for _, field := range []string{"token", "actor", "path", "intent", "revision"} {
			a := admit(t)
			before := snapshot(a)
			switch field {
			case "token":
				a.ExecutionToken = ""
			case "actor":
				a.UserID++
			case "path":
				a.Path = "/settings"
			case "intent":
				a.IntentHash = "changed"
			case "revision":
				a.Revision = "changed"
			}
			if err := execute(a); err == nil {
				t.Fatalf("accepted %s", field)
			}
			if after := snapshot(a); after != before {
				t.Fatalf("mutation on %s refusal", field)
			}
		}
	})
	t.Run("audit-failure", func(t *testing.T) {
		a := admit(t)
		before := snapshot(a)
		if _, err := pool.Exec(ctx, `CREATE FUNCTION reject_email_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER reject_email_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION reject_email_audit()`); err != nil {
			t.Fatal(err)
		}
		if err := execute(a); err == nil {
			t.Fatal("audit failure accepted")
		}
		if snapshot(a) != before {
			t.Fatal("audit failure partially committed")
		}
		if _, err := pool.Exec(ctx, `DROP TRIGGER reject_email_audit ON authorization_audit`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("receipt-suppression", func(t *testing.T) {
		a := admit(t)
		before := snapshot(a)
		if _, err := pool.Exec(ctx, `CREATE FUNCTION suppress_email_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.domain_committed_at IS NOT NULL THEN RETURN NULL; END IF; RETURN NEW; END $$; CREATE TRIGGER suppress_email_receipt BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION suppress_email_receipt()`); err != nil {
			t.Fatal(err)
		}
		if err := execute(a); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("suppressed receipt %v", err)
		}
		if snapshot(a) != before {
			t.Fatal("policy/audit/effect survived receipt suppression")
		}
		if _, err := pool.Exec(ctx, `DROP TRIGGER suppress_email_receipt ON online_save_actions`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("reconcile-paused-dispatch-and-token-CAS", func(t *testing.T) {
		a := admit(t)
		if outcome, err := authority.ReconcileEmailEnable(ctx, actor, a); err != nil || outcome != "rejected" {
			t.Fatalf("reconcile %s %v", outcome, err)
		}
		before := snapshot(a)
		if err := execute(a); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("late original %v", err)
		}
		if snapshot(a) != before {
			t.Fatal("fenced original mutated")
		}
		retry := a
		retry.ExecutionToken = uuid.NewString()
		if err := authority.RetryEmailEnable(ctx, actor, retry, a.ExecutionToken); err != nil {
			t.Fatal(err)
		}
		if err := authority.RetryEmailEnable(ctx, actor, retry, a.ExecutionToken); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("second CAS %v", err)
		}
		if _, err := authority.ReconcileEmailEnable(ctx, actor, a); err == nil {
			t.Fatal("obsolete reconciliation fenced retry")
		}
		if err := execute(a); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatal(err)
		}
		if err := execute(retry); err != nil {
			t.Fatal(err)
		}
		r, err := store.Receipt(ctx, retry)
		if err != nil || r.Disposition != "saved" || r.CommittedSteps != 1 {
			t.Fatalf("receipt %+v %v", r, err)
		}
		p, err := emailpolicy.Read(ctx, pool, false)
		if err != nil || !p.Enabled || p.LastEnableOperation == nil || *p.LastEnableOperation != a.ID {
			t.Fatalf("policy %+v %v", p, err)
		}
		before = snapshot(retry)
		if outcome, err := authority.ReconcileEmailEnable(ctx, actor, retry); err != nil || outcome != "saved" {
			t.Fatalf("committed reconcile %s %v", outcome, err)
		}
		if snapshot(retry) != before {
			t.Fatal("committed reconciliation mutated")
		}
	})
	t.Run("no-change", func(t *testing.T) {
		a := admit(t)
		if err := execute(a); err != nil {
			t.Fatal(err)
		}
		r, err := store.Receipt(ctx, a)
		if err != nil || r.Disposition != "completed" || r.CommittedSteps != 0 {
			t.Fatalf("no-change %+v %v", r, err)
		}
		if outcome, err := authority.ReconcileEmailEnable(ctx, actor, a); err != nil || outcome != "completed" {
			t.Fatalf("no-change reconcile %s %v", outcome, err)
		}
	})
	t.Run("no-change-receipt-suppression", func(t *testing.T) {
		a := admit(t)
		before := snapshot(a)
		if _, err := pool.Exec(ctx, `CREATE TRIGGER suppress_email_receipt BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION suppress_email_receipt()`); err != nil {
			t.Fatal(err)
		}
		if err := execute(a); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("suppressed no-change receipt %v", err)
		}
		if snapshot(a) != before {
			t.Fatal("suppressed no-change changed durable state")
		}
		if _, err := pool.Exec(ctx, `DROP TRIGGER suppress_email_receipt ON online_save_actions`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("original-transaction-wins-reconciliation-waits", func(t *testing.T) {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if _, err := pool.Exec(c, `UPDATE restore_email_policy SET enabled=false,revision=revision+1,last_enable_operation=NULL;
		CREATE FUNCTION pause_email_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(941012); RETURN NEW; END $$;
		CREATE TRIGGER pause_email_audit BEFORE INSERT ON authorization_audit FOR EACH ROW EXECUTE FUNCTION pause_email_audit()`); err != nil {
			t.Fatal(err)
		}
		a := admit(t)
		blocker, err := pool.Acquire(c)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Release()
		if _, err := blocker.Exec(c, `SELECT pg_advisory_lock(941012)`); err != nil {
			t.Fatal(err)
		}
		defer blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(941012)`)
		done := make(chan error, 1)
		go func() { done <- authority.EnableEmail(saveaction.WithAction(c, a), actor) }()
		wait := func(query string, args ...any) {
			for {
				var waiting bool
				if err := pool.QueryRow(c, query, args...).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					return
				}
				select {
				case <-c.Done():
					t.Fatal(c.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
		}
		wait(`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE objid=941012 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`)
		cfg := pool.Config()
		label := "email-reconcile-" + uuid.NewString()
		cfg.ConnConfig.RuntimeParams["application_name"] = label
		reconcilePool, err := pgxpool.NewWithConfig(c, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer reconcilePool.Close()
		type resolution struct {
			outcome string
			err     error
		}
		resolved := make(chan resolution, 1)
		go func() {
			outcome, err := authorization.New(reconcilePool).ReconcileEmailEnable(c, actor, a)
			resolved <- resolution{outcome, err}
		}()
		wait(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE application_name=$1 AND wait_event='advisory')`, label)
		if _, err := blocker.Exec(c, `SELECT pg_advisory_unlock(941012)`); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		result := <-resolved
		if result.err != nil || result.outcome != "saved" {
			t.Fatalf("waiting reconciliation %+v", result)
		}
		if _, err := pool.Exec(c, `DROP TRIGGER pause_email_audit ON authorization_audit`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing-history", func(t *testing.T) {
		a := admit(t)
		a.ID = uuid.NewString()
		if outcome, err := authority.ReconcileEmailEnable(ctx, actor, a); err != nil || outcome != "unknown" {
			t.Fatalf("missing %s %v", outcome, err)
		}
	})
	t.Run("lost-admission-response", func(t *testing.T) {
		p, err := emailpolicy.Read(ctx, pool, false)
		if err != nil {
			t.Fatal(err)
		}
		a := saveaction.Action{UserID: 941, ID: uuid.NewString(), ExecutionToken: uuid.NewString(), Path: authorization.EmailEnablePath, Revision: p.Review(), IntentHash: "lost-admission", Atomic: true, NoChange: true}
		lost := emailResponseLossPool(t, pool, "INSERT 0 1\x00")
		if ok, err := (saveaction.Store{Pool: lost}).AdmitExecution(ctx, a, a.IntentHash, "POST", a.Path, "/settings/mail"); err == nil || ok {
			t.Fatalf("admission response not lost %v %v", ok, err)
		}
		if err := store.Complete(ctx, a, 503, "", true); err != nil {
			t.Fatal(err)
		}
		r, err := store.Receipt(ctx, a)
		if err != nil || r.Disposition != "unknown" {
			t.Fatalf("ambiguous admission sealed %+v %v", r, err)
		}
		if outcome, err := authority.ReconcileEmailEnable(ctx, actor, a); err != nil || outcome != "rejected" {
			t.Fatalf("reconcile %s %v", outcome, err)
		}
		if err := execute(a); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("lost-admit original ran %v", err)
		}
	})
	t.Run("simultaneous-retry-CAS", func(t *testing.T) {
		a := admit(t)
		if _, err := authority.ReconcileEmailEnable(ctx, actor, a); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		type result struct {
			action saveaction.Action
			err    error
		}
		results := make(chan result, 2)
		for range 2 {
			retry := a
			retry.ExecutionToken = uuid.NewString()
			go func() {
				<-start
				results <- result{retry, authority.RetryEmailEnable(ctx, actor, retry, a.ExecutionToken)}
			}()
		}
		close(start)
		first, second := <-results, <-results
		if first.err != nil {
			first, second = second, first
		}
		if first.err != nil || !errors.Is(second.err, saveaction.ErrExecutionFenced) {
			t.Fatalf("CAS winners %v %v", first.err, second.err)
		}
		if err := execute(second.action); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("loser executed %v", err)
		}
		if err := execute(first.action); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("changed-generation", func(t *testing.T) {
		a := admit(t)
		if _, err := pool.Exec(ctx, `UPDATE session_instance SET epoch=gen_random_uuid(); UPDATE restore_email_policy SET epoch=(SELECT epoch FROM session_instance),revision=revision+1`); err != nil {
			t.Fatal(err)
		}
		before := snapshot(a)
		if err := execute(a); !errors.Is(err, authorization.ErrConflict) {
			t.Fatalf("old epoch %v", err)
		}
		if snapshot(a) != before {
			t.Fatal("old epoch changed policy")
		}
	})
	t.Run("lost-commit-response", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `UPDATE restore_email_policy SET enabled=false,revision=revision+1,last_enable_operation=NULL`); err != nil {
			t.Fatal(err)
		}
		a := admit(t)
		lost := emailResponseLossPool(t, pool, "COMMIT\x00")
		err := authorization.New(lost).EnableEmail(saveaction.WithAction(ctx, a), actor)
		var failure *authorization.TransactionFailure
		if !errors.As(err, &failure) || failure.RolledBack {
			t.Fatalf("lost COMMIT not unresolved: %v", err)
		}
		if err := store.Complete(ctx, a, 503, "", true); err != nil {
			t.Fatal(err)
		}
		r, err := store.Receipt(ctx, a)
		if err != nil || r.Disposition != "saved" || r.CommittedSteps != 1 {
			t.Fatalf("durable commit %+v %v", r, err)
		}
		if outcome, err := authority.ReconcileEmailEnable(ctx, actor, a); err != nil || outcome != "saved" {
			t.Fatalf("reconcile %s %v", outcome, err)
		}
	})
	t.Run("protected-authority-revoked", func(t *testing.T) {
		a := admit(t)
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 942,id,'guardian@test','Guardian','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin',1 FROM companies LIMIT 1;
		UPDATE users SET authorization_role_id=2 WHERE id=941`); err != nil {
			t.Fatal(err)
		}
		before := snapshot(a)
		if err := execute(a); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("revoked authority %v", err)
		}
		if _, err := authority.ReconcileEmailEnable(ctx, actor, a); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("revoked authority reconciled %v", err)
		}
		if snapshot(a) != before {
			t.Fatal("revoked authority changed policy/receipt")
		}
		if _, err := pool.Exec(ctx, `UPDATE users SET authorization_role_id=1 WHERE id=941`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("session-revoked", func(t *testing.T) {
		a := admit(t)
		before := snapshot(a)
		if _, err := pool.Exec(ctx, `UPDATE sessions SET revoked_at=clock_timestamp() WHERE token_hash='policy-session'`); err != nil {
			t.Fatal(err)
		}
		if err := execute(a); !errors.Is(err, authorization.ErrForbidden) {
			t.Fatalf("revoked %v", err)
		}
		if snapshot(a) != before {
			t.Fatal("revoked session mutated")
		}
	})
}

// The real server executes the command. This socket drops its actual successful
// CommandComplete response, rather than replacing a service return value.
type emailLossConn struct {
	net.Conn
	tag     []byte
	seen    []byte
	dropped *atomic.Bool
}

func (c *emailLossConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if c.dropped.Load() {
		return n, err
	}
	c.seen = append(c.seen, p[:n]...)
	if bytes.Contains(c.seen, c.tag) && c.dropped.CompareAndSwap(false, true) {
		_ = c.Conn.Close()
		return 0, io.ErrUnexpectedEOF
	}
	if len(c.seen) > len(c.tag) {
		c.seen = append([]byte(nil), c.seen[len(c.seen)-len(c.tag):]...)
	}
	return n, err
}

func emailResponseLossPool(t *testing.T, source *pgxpool.Pool, tag string) *pgxpool.Pool {
	t.Helper()
	config := source.Config()
	config.MaxConns = 1
	dropped := &atomic.Bool{}
	config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &emailLossConn{Conn: conn, tag: []byte(tag), dropped: dropped}, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if !dropped.Load() {
			t.Error("response loss boundary never reached")
		}
	})
	return pool
}

// This cluster-global fault runs ONLY as a separately selected, explicitly
// enabled disposable-cluster test, never beside other package tests.
func TestEmailDurabilityRejection(t *testing.T) {
	if os.Getenv("FREEFSM_POLICY_DURABILITY_TEST") != "1" {
		t.Skip("requires isolated disposable-cluster durability fault authorization")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	pool, _ := fixture(t)
	set := func(setting, value string) {
		t.Helper()
		if _, err := admin.Exec(ctx, "ALTER SYSTEM SET "+setting+" = '"+value+"'"); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, `SELECT pg_reload_conf()`); err != nil {
			t.Fatal(err)
		}
		for {
			var got string
			if err := pool.QueryRow(ctx, `SELECT current_setting($1)`, setting).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got == value {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	for _, setting := range []string{"fsync", "full_page_writes"} {
		t.Run(setting, func(t *testing.T) {
			set(setting, "off")
			defer set(setting, "on")
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if err = emailpolicy.Durable(ctx, tx); !errors.Is(err, emailpolicy.ErrUnavailable) {
				t.Fatalf("unsafe setting accepted: %v", err)
			}
		})
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SET LOCAL synchronous_commit=off`); err != nil {
		t.Fatal(err)
	}
	if err = emailpolicy.Durable(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var synchronous string
	if err = tx.QueryRow(ctx, `SHOW synchronous_commit`).Scan(&synchronous); err != nil || synchronous != "on" {
		t.Fatalf("durable commit %s %v", synchronous, err)
	}
}
