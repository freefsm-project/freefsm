package authorization_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/protectedaudit"
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
	// Include the durable row itself, not just its public projection: rejected
	// callbacks must not alter completion timestamps or attempt ownership either.
	snapshot := func(t *testing.T, a saveaction.Action) string {
		t.Helper()
		var value string
		if err := pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(a),
		(SELECT count(*) FROM execution_probe WHERE operation_id=a.operation_id),
		(SELECT count(*) FROM online_save_effects WHERE operation_id=a.operation_id),
		(SELECT count(*) FROM authorization_audit WHERE detail->>'operation_id'=a.operation_id::text))::text
		FROM online_save_actions a WHERE user_id=$1 AND operation_id=$2`, a.UserID, a.ID).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	mutate := func(c context.Context, tx pgx.Tx, a saveaction.Action) error {
		if _, err := tx.Exec(c, `INSERT INTO execution_probe VALUES($1)`, a.ID); err != nil {
			return err
		}
		return protectedaudit.RecordProduct(c, tx, protectedaudit.ProductAction{
			ActorID: a.UserID, RoleID: 1, Action: "email.enable", Scope: "S", TargetType: "execution_probe", TargetID: 1,
			Channel: "web", Detail: map[string]string{"operation_id": a.ID},
		})
	}
	for _, state := range []string{"admitted", "fenced", "replaced"} {
		t.Run("missing-token-"+state, func(t *testing.T) {
			a := admit(t)
			if state != "admitted" {
				tx := begin(t)
				if _, err := saveaction.FenceExecution(ctx, tx, a.UserID, a.ID, a.Path); err != nil {
					t.Fatal(err)
				}
				if state == "replaced" {
					retry := a
					retry.ExecutionToken = uuid.NewString()
					if err := saveaction.RetryExecution(ctx, tx, retry, a.ExecutionToken, "intent"); err != nil {
						t.Fatal(err)
					}
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshot(t, a)
			a.ExecutionToken = "" // simulate the original adapter losing its token
			c := saveaction.WithAction(ctx, a)
			called := false
			err := authority.WithinActor(c, authorization.Actor{UserID: a.UserID}, func(tx pgx.Tx, _ authorization.Principal) error {
				called = true
				return mutate(c, tx, a)
			})
			if !errors.Is(err, saveaction.ErrExecutionFenced) || called {
				t.Errorf("missing-token callback: called=%v err=%v", called, err)
			}
			if after := snapshot(t, a); after != before {
				t.Errorf("domain/audit/effect/receipt mutated: before=%s after=%s", before, after)
			}
		})
	}
	t.Run("correct-token-authorization-success-and-denial", func(t *testing.T) {
		for _, deny := range []bool{true, false} {
			a := admit(t)
			a.ExecutionToken = strings.ToUpper(a.ExecutionToken) // PostgreSQL UUID identity, not spelling
			before := snapshot(t, a)
			c := saveaction.WithAction(ctx, a)
			err := authority.WithinActor(c, authorization.Actor{UserID: a.UserID}, func(tx pgx.Tx, _ authorization.Principal) error {
				if deny {
					return authorization.ErrForbidden
				}
				return mutate(c, tx, a)
			})
			if deny {
				if !errors.Is(err, authorization.ErrForbidden) || snapshot(t, a) != before {
					t.Fatalf("denial mutated state: %v", err)
				}
			} else {
				r, receiptErr := store.Receipt(ctx, a)
				if err != nil || receiptErr != nil || r.Disposition != "saved" || r.CommittedSteps != 1 {
					t.Fatalf("success: %+v %v %v", r, err, receiptErr)
				}
			}
		}
	})
	t.Run("missing-token-and-atomic-flag-still-fenced", func(t *testing.T) {
		a := admit(t)
		before := snapshot(t, a)
		a.ExecutionToken, a.Atomic = "", false
		c := saveaction.WithAction(ctx, a)
		called := false
		if err := authority.WithinActor(c, authorization.Actor{UserID: a.UserID}, func(tx pgx.Tx, _ authorization.Principal) error {
			called = true
			return mutate(c, tx, a)
		}); !errors.Is(err, saveaction.ErrExecutionFenced) || called || snapshot(t, a) != before {
			t.Fatalf("lost local flags: called=%v err=%v", called, err)
		}
	})
	t.Run("lost-token-at-effect-and-receipt-seams", func(t *testing.T) {
		for name, seal := range map[string]func(context.Context, pgx.Tx) error{
			"effect": func(c context.Context, tx pgx.Tx) error {
				return saveaction.RecordEffect(c, tx, 941, "email.enable", "execution_probe", 1)
			},
			"domain":    saveaction.ConfirmDomain,
			"no-change": saveaction.ConfirmNoChange,
		} {
			t.Run(name, func(t *testing.T) {
				a := admit(t)
				before := snapshot(t, a)
				tx := begin(t)
				if err := saveaction.LockExecution(saveaction.WithAction(ctx, a), tx); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO execution_probe VALUES($1)`, a.ID); err != nil {
					t.Fatal(err)
				}
				a.ExecutionToken, a.NoChange = "", true
				if err := seal(saveaction.WithAction(ctx, a), tx); !errors.Is(err, saveaction.ErrExecutionFenced) {
					t.Errorf("lost token accepted by %s: %v", name, err)
				}
				_ = tx.Rollback(ctx)
				if snapshot(t, a) != before {
					t.Fatal("partial domain/effect/receipt persisted")
				}
			})
		}
	})
	t.Run("legacy-null-token-and-absent-action", func(t *testing.T) {
		for _, noChange := range []bool{false, true} {
			a := saveaction.Action{UserID: 941, ID: uuid.NewString(), Atomic: true, NoChange: noChange, Path: "/legacy"}
			if ok, err := store.Admit(ctx, a, "legacy", "POST", a.Path, "/"); err != nil || !ok {
				t.Fatalf("legacy admit: %v %v", ok, err)
			}
			c := saveaction.WithAction(ctx, a)
			if err := authority.WithinActor(c, authorization.Actor{UserID: a.UserID}, func(tx pgx.Tx, _ authorization.Principal) error {
				if noChange {
					return saveaction.ConfirmNoChange(c, tx)
				}
				return mutate(c, tx, a)
			}); err != nil {
				t.Fatal(err)
			}
			r, err := store.Receipt(ctx, a)
			want, steps := "saved", 1
			if noChange {
				want, steps = "completed", 0
			}
			if err != nil || r.Disposition != want || r.CommittedSteps != steps {
				t.Fatalf("legacy: %+v %v", r, err)
			}
		}
		called := false
		if err := authority.WithinActor(ctx, authorization.Actor{UserID: 941}, func(pgx.Tx, authorization.Principal) error {
			called = true
			return nil
		}); err != nil || !called {
			t.Fatalf("absent generic action: %v %v", called, err)
		}
	})
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
		_ = tx.Rollback(ctx)
		before := snapshot(t, a)
		called := false
		c := saveaction.WithAction(ctx, a)
		if err := authority.WithinActor(c, authorization.Actor{UserID: a.UserID}, func(tx pgx.Tx, _ authorization.Principal) error {
			called = true
			return mutate(c, tx, a)
		}); !errors.Is(err, saveaction.ErrExecutionFenced) || called || snapshot(t, a) != before {
			t.Fatalf("obsolete token callback: called=%v err=%v", called, err)
		}
		tx = begin(t)
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
		before := snapshot(t, a)
		tx = begin(t)
		result, err := saveaction.FenceExecution(ctx, tx, a.UserID, a.ID, a.Path)
		if err != nil || result != r.Disposition {
			t.Errorf("reconcile no-change: %s receipt=%s err=%v", result, r.Disposition, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		after, err := store.Receipt(ctx, a)
		if err != nil || !reflect.DeepEqual(after, r) || snapshot(t, a) != before {
			t.Fatalf("reconciliation rewrote history: %+v %v", after, err)
		}
	})
	t.Run("no-change-zero-rows-rolls-back", func(t *testing.T) {
		a := admit(t)
		a.NoChange = true
		before := snapshot(t, a)
		c := saveaction.WithAction(ctx, a)
		err := authority.WithinActor(c, authorization.Actor{UserID: a.UserID}, func(tx pgx.Tx, _ authorization.Principal) error {
			// Transactional sentinel proves rollback, without recording a domain
			// effect that would itself disqualify a no-change confirmation.
			if _, err := tx.Exec(ctx, `INSERT INTO execution_probe VALUES($1)`, a.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `CREATE FUNCTION suppress_no_change_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.domain_outcome='completed' THEN RETURN NULL; END IF; RETURN NEW; END $$; CREATE TRIGGER suppress_no_change_receipt BEFORE UPDATE ON online_save_actions FOR EACH ROW EXECUTE FUNCTION suppress_no_change_receipt()`); err != nil {
				return err
			}
			return saveaction.ConfirmNoChange(c, tx)
		})
		if !errors.Is(err, saveaction.ErrExecutionFenced) || snapshot(t, a) != before {
			t.Fatalf("suppressed no-change was not rolled back: %v", err)
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
