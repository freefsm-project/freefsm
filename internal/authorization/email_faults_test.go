package authorization_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
)

// A deferred constraint fails inside the real server's COMMIT, after the
// command has written policy, audit and receipt. The socket discards that error
// response. Reconciliation must wait for this transaction, then fence its exact
// original token; a missing receipt while COMMIT is blocked proves nothing.
func TestEmailLostCommitRollbackReconciliation(t *testing.T) {
	pool, authority := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 941,id,'fault@test','Fault','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin',1 FROM companies LIMIT 1;
	INSERT INTO sessions(user_id,token_hash,kind,expires_at) VALUES(941,'fault-session','web',clock_timestamp()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = emailpolicy.Import(ctx, tx, func() (bool, error) { return true, nil }); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	policy, err := emailpolicy.Read(ctx, pool, false)
	if err != nil {
		t.Fatal(err)
	}
	a := saveaction.Action{UserID: 941, ID: uuid.NewString(), ExecutionToken: uuid.NewString(), Path: authorization.EmailEnablePath, Revision: policy.Review(), IntentHash: "fault:" + policy.Review(), Atomic: true, NoChange: true}
	actor := authorization.Actor{UserID: 941, SessionHash: "fault-session", Channel: "web"}
	store := saveaction.Store{Pool: pool}
	if ok, err := store.AdmitExecution(ctx, a, a.IntentHash, "POST", a.Path, "/settings/mail"); err != nil || !ok {
		t.Fatalf("admission: %v %v", ok, err)
	}
	if _, err := pool.Exec(ctx, `CREATE FUNCTION fail_email_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(941099); RAISE EXCEPTION 'email_commit_rollback_fault'; END $$;
	CREATE CONSTRAINT TRIGGER fail_email_commit AFTER INSERT ON authorization_audit DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_email_commit()`); err != nil {
		t.Fatal(err)
	}
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_lock(941099)`); err != nil {
		t.Fatal(err)
	}
	defer blocker.Exec(context.Background(), `SELECT pg_advisory_unlock(941099)`)
	lost := emailResponseLossPool(t, pool, "email_commit_rollback_fault")
	done := make(chan error, 1)
	go func() { done <- authorization.New(lost).EnableEmail(saveaction.WithAction(ctx, a), actor) }()
	wait := func(query string) {
		t.Helper()
		for {
			var ready bool
			if err := pool.QueryRow(ctx, query).Scan(&ready); err != nil {
				t.Fatal(err)
			}
			if ready {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	wait(`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE objid=941099 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`)
	if r, err := store.Receipt(ctx, a); err != nil || r.Disposition != "unknown" {
		t.Fatalf("running COMMIT falsely terminal: %+v %v", r, err)
	}
	type resolution struct {
		outcome string
		err     error
	}
	resolved := make(chan resolution, 1)
	go func() {
		outcome, err := authority.ReconcileEmailEnable(ctx, actor, a)
		resolved <- resolution{outcome, err}
	}()
	wait(`SELECT EXISTS(SELECT 1 FROM pg_locks WHERE objid=190019 AND NOT granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`)
	select {
	case r := <-resolved:
		t.Fatalf("reconciled before rollback: %+v", r)
	default:
	}
	if _, err := blocker.Exec(ctx, `SELECT pg_advisory_unlock(941099)`); err != nil {
		t.Fatal(err)
	}
	err = <-done
	var failure *authorization.TransactionFailure
	if !errors.As(err, &failure) || failure.RolledBack {
		t.Fatalf("lost rollback response must remain unknown to original: %v", err)
	}
	r := <-resolved
	if r.err != nil || r.outcome != "rejected" {
		t.Fatalf("terminal reconciliation: %+v", r)
	}
	var audit, effects int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=$1::text),(SELECT count(*) FROM online_save_effects WHERE operation_id=$1::uuid)`, a.ID).Scan(&audit, &effects); err != nil {
		t.Fatal(err)
	}
	after, err := emailpolicy.Read(ctx, pool, false)
	if err != nil || after != policy || audit != 0 || effects != 0 {
		t.Fatalf("partial commit: policy=%+v prior=%+v audit=%d effects=%d err=%v", after, policy, audit, effects, err)
	}
	if err := authority.EnableEmail(saveaction.WithAction(ctx, a), actor); !errors.Is(err, saveaction.ErrExecutionFenced) {
		t.Fatalf("late original escaped fence: %v", err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_email_commit ON authorization_audit`); err != nil {
		t.Fatal(err)
	}
	retry := a
	retry.ExecutionToken = uuid.NewString()
	if err := authority.RetryEmailEnable(ctx, actor, retry, a.ExecutionToken); err != nil {
		t.Fatal(err)
	}
	if err := authority.EnableEmail(saveaction.WithAction(ctx, a), actor); !errors.Is(err, saveaction.ErrExecutionFenced) {
		t.Fatalf("original escaped replacement token: %v", err)
	}
	if err := authority.EnableEmail(saveaction.WithAction(ctx, retry), actor); err != nil {
		t.Fatal(err)
	}
	if r, err := store.Receipt(ctx, retry); err != nil || r.Disposition != "saved" || r.CommittedSteps != 1 {
		t.Fatalf("same-ID retry: %+v %v", r, err)
	}
}
