package authorization_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// These tests qualify the existing receipt owner's execution primitive with a
// real database. The probe is not the email policy or its protected audit.
func TestEmailExecutionFence(t *testing.T) {
	pool, authority := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 941,id,'execution@test','Execution','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin',1 FROM companies LIMIT 1;
	CREATE TABLE execution_probe(operation_id uuid PRIMARY KEY)`)
	if err != nil {
		t.Fatal(err)
	}
	store := saveaction.Store{Pool: pool}
	admit := func(t *testing.T) saveaction.Action {
		t.Helper()
		a := saveaction.Action{UserID: 941, ID: uuid.NewString(), Path: "/settings/enable-email", ExecutionToken: uuid.NewString(), Atomic: true, Outcome: &saveaction.Outcome{}}
		if ok, err := store.AdmitExecution(ctx, a, "intent", "POST", a.Path, "/settings/mail"); err != nil || !ok {
			t.Fatalf("admit: %v %v", ok, err)
		}
		return a
	}
	begin := func(t *testing.T) pgx.Tx {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	execute := func(tx pgx.Tx, a saveaction.Action) error {
		c := saveaction.WithAction(ctx, a)
		if err := saveaction.LockExecution(c, tx); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO execution_probe VALUES($1)`, a.ID); err != nil {
			return err
		}
		if err := saveaction.RecordEffect(c, tx, a.UserID, "email.enable", "execution_probe", 1); err != nil {
			return err
		}
		return saveaction.ConfirmDomain(c, tx)
	}
	t.Run("reconciliation-wins-before-authority", func(t *testing.T) {
		a := admit(t) // original dispatch is paused here, before authority
		tx := begin(t)
		if result, err := saveaction.FenceExecution(ctx, tx, a.UserID, a.ID, a.Path); err != nil || result != "rejected" {
			t.Fatalf("fence: %s %v", result, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx = begin(t) // resume the original admitted dispatch
		if err := execute(tx, a); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("late dispatch: %v", err)
		}
		_ = tx.Rollback(ctx)
		called := false
		if err := authority.WithinActor(saveaction.WithAction(ctx, a), authorization.Actor{UserID: a.UserID}, func(pgx.Tx, authorization.Principal) error {
			called = true
			return nil
		}); !errors.Is(err, saveaction.ErrExecutionFenced) || called {
			t.Fatalf("authorization boundary dispatched fenced action: called=%v err=%v", called, err)
		}
		// Neither a typed completion nor an adapter that lost its token can
		// overwrite the reconciliation decision or create retry permission.
		for _, token := range []string{a.ExecutionToken, ""} {
			copy := a
			copy.ExecutionToken = token
			if err := store.Complete(ctx, copy, 503, "", true); err != nil {
				t.Fatal(err)
			}
		}
		r, err := store.Receipt(ctx, a)
		if err != nil || r.Disposition != "rejected" || r.CommittedSteps != 0 || r.RetrySafe || r.RetrySameOperation {
			t.Fatalf("receipt: %+v %v", r, err)
		}
	})
	t.Run("execution-wins-before-reconciliation", func(t *testing.T) {
		a := admit(t)
		tx := begin(t)
		if err := execute(tx, a); err != nil {
			t.Fatal(err)
		}
		started, done := make(chan struct{}), make(chan error, 1)
		go func() {
			other, err := pool.Begin(ctx)
			if err != nil {
				done <- err
				return
			}
			defer other.Rollback(ctx)
			close(started)
			if _, err = other.Exec(ctx, `SELECT pg_advisory_xact_lock(190019)`); err == nil {
				var result string
				result, err = saveaction.FenceExecution(ctx, other, a.UserID, a.ID, a.Path)
				if err == nil && result != "saved" {
					err = errors.New("committed execution was classified as nonexecution")
				}
			}
			done <- err
		}()
		<-started
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
	t.Run("retry-token-fences-original-and-competing-retry", func(t *testing.T) {
		a := admit(t)
		tx := begin(t)
		if _, err := saveaction.FenceExecution(ctx, tx, a.UserID, a.ID, a.Path); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		retry := a
		retry.ExecutionToken = uuid.NewString()
		tx = begin(t)
		if err := saveaction.RetryExecution(ctx, tx, retry, a.ExecutionToken, "intent"); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx = begin(t)
		if err := execute(tx, a); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("original dispatch: %v", err)
		}
		competitor := a
		competitor.ExecutionToken = uuid.NewString()
		if err := saveaction.RetryExecution(ctx, tx, competitor, a.ExecutionToken, "intent"); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("competing retry: %v", err)
		}
		if err := execute(tx, retry); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("duplicate-changed-intent-and-missing-history", func(t *testing.T) {
		a := admit(t)
		if ok, err := store.AdmitExecution(ctx, a, "intent", "POST", a.Path, "/settings/mail"); err != nil || ok {
			t.Fatalf("duplicate dispatched: %v %v", ok, err)
		}
		if _, err := store.AdmitExecution(ctx, a, "changed", "POST", a.Path, "/settings/mail"); !errors.Is(err, saveaction.ErrIdentity) {
			t.Fatalf("changed intent: %v", err)
		}
		tx := begin(t)
		if result, err := saveaction.FenceExecution(ctx, tx, a.UserID, uuid.NewString(), a.Path); err != nil || result != "unknown" {
			t.Fatalf("missing history: %s %v", result, err)
		}
	})
	t.Run("unknown-survives-generic-completion", func(t *testing.T) {
		a := admit(t)
		copy := a
		copy.ExecutionToken = ""
		copy.Outcome.RejectedBeforeWrite.Store(true)
		copy.RetryTransient = true
		for _, status := range []int{503, 403, 303} {
			if err := store.Complete(ctx, copy, status, "/settings/mail", true); err != nil {
				t.Fatal(err)
			}
		}
		r, err := store.Receipt(ctx, a)
		if err != nil || r.Disposition != "unknown" || r.RetrySafe || r.RetrySameOperation {
			t.Fatalf("unknown completed: %+v %v", r, err)
		}
	})
	t.Run("verified-no-change-receipt", func(t *testing.T) {
		a := admit(t)
		a.NoChange = true
		c := saveaction.WithAction(ctx, a)
		tx := begin(t)
		if err := saveaction.LockExecution(c, tx); err != nil {
			t.Fatal(err)
		}
		if err := saveaction.ConfirmNoChange(c, tx); err != nil {
			t.Fatal(err)
		}
		if err := saveaction.ConfirmDomain(c, tx); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		r, err := store.Receipt(ctx, a)
		if err != nil || r.Disposition != "completed" || r.CommittedSteps != 0 {
			t.Fatalf("no change: %+v %v", r, err)
		}
	})
	t.Run("receipt-zero-rows-rolls-back-effect", func(t *testing.T) {
		a := admit(t)
		tx := begin(t)
		if _, err := tx.Exec(ctx, `CREATE FUNCTION suppress_execution_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.domain_committed_at IS NOT NULL THEN RETURN NULL; END IF; RETURN NEW; END $$; CREATE TRIGGER suppress_execution_receipt BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION suppress_execution_receipt()`); err != nil {
			t.Fatal(err)
		}
		if err := execute(tx, a); !errors.Is(err, saveaction.ErrExecutionFenced) {
			t.Fatalf("zero-row receipt accepted: %v", err)
		}
		_ = tx.Rollback(ctx)
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM execution_probe WHERE operation_id=$1`, a.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial effect: %d %v", count, err)
		}
		r, err := store.Receipt(ctx, a)
		if err != nil || r.CommittedSteps != 0 || r.Disposition != "unknown" {
			t.Fatalf("partial receipt: %+v %v", r, err)
		}
	})
}
