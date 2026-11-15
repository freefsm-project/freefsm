package backup

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/freefsm-project/freefsm/internal/authorization"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/saveaction"
	"github.com/google/uuid"
)

func TestModernEmailEncryptedRestore(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, rollback := range []bool{false, true} {
			name := "disabled/commit"
			if enabled {
				name = "enabled/commit"
			}
			if rollback {
				name += "-rollback"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				m, db := preflightFixture(t)
				startup, err := m.BeginStartup(ctx)
				if err != nil {
					t.Fatal(err)
				}
				tx, err := db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err = emailpolicy.Import(ctx, tx, func() (bool, error) { return true, nil }); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err = startup.Finish(ctx); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Pool.Exec(ctx, `INSERT INTO users(id,company_id,email,name,password_hash,role,authorization_role_id) SELECT 941,id,'restore-policy@test','Policy','$2a$10$abcdefghijklmnopqrstuu1234567890123456789012345678901','admin',1 FROM companies LIMIT 1;
				INSERT INTO sessions(user_id,token_hash,kind,expires_at) VALUES(941,'archive-policy-session','web',clock_timestamp()+interval '1 hour');
				CREATE TABLE policy_sentinel(value text); INSERT INTO policy_sentinel VALUES('source')`); err != nil {
					t.Fatal(err)
				}
				p, err := emailpolicy.Read(ctx, db.Pool, false)
				if err != nil {
					t.Fatal(err)
				}
				a := saveaction.Action{UserID: 941, ID: uuid.NewString(), ExecutionToken: uuid.NewString(), Path: authorization.EmailEnablePath, Revision: p.Review(), IntentHash: "enable", Atomic: true, NoChange: true}
				if ok, err := (saveaction.Store{Pool: db.Pool}).AdmitExecution(ctx, a, a.IntentHash, "POST", a.Path, "/settings/mail"); err != nil || !ok {
					t.Fatalf("admit %v %v", ok, err)
				}
				if err := authorization.New(db.Pool).EnableEmail(saveaction.WithAction(ctx, a), authorization.Actor{UserID: 941, SessionHash: "archive-policy-session", Channel: "web"}); err != nil {
					t.Fatal(err)
				}
				file := filepath.Join(m.cfg.UploadDir, "policy-sentinel.txt")
				if err = os.WriteFile(file, []byte("source"), 0600); err != nil {
					t.Fatal(err)
				}
				archive := createCrashArchive(t, m)
				if _, err = db.Pool.Exec(ctx, `UPDATE restore_email_policy SET enabled=$1`, enabled); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Pool.Exec(ctx, `UPDATE policy_sentinel SET value='destination'; INSERT INTO sessions(user_id,token_hash,kind,expires_at) VALUES(941,'destination-policy-session','web',clock_timestamp()+interval '1 hour')`); err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(file, []byte("destination"), 0600); err != nil {
					t.Fatal(err)
				}
				snapshot := func() string {
					var value string
					if err := db.Pool.QueryRow(ctx, `SELECT jsonb_build_array((SELECT to_jsonb(p) FROM restore_email_policy p),(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM authorization_audit a),(SELECT jsonb_agg(to_jsonb(a) ORDER BY operation_id) FROM online_save_actions a),(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM online_save_effects e))::text`).Scan(&value); err != nil {
						t.Fatal(err)
					}
					return value
				}
				before := snapshot()
				op := stageCrashArchive(t, m, archive)
				hit := false
				if rollback {
					m.checkpoint = func(point string) error {
						if point == "protections-applied" {
							hit = true
							return syscall.EIO
						}
						return nil
					}
				}
				if _, err = m.StartRestore(ctx, 1, op.ID, "destination", "destination"); err != nil {
					t.Fatal(err)
				}
				op = awaitOperation(t, m, 1, op.ID)
				m.checkpoint = nil
				if rollback && !hit {
					t.Fatal("protection boundary not reached")
				}
				if rollback && op.Phase == "complete" {
					t.Fatal("rollback falsely complete")
				}
				if !rollback && op.Phase != "complete" {
					t.Fatalf("restore %+v", op)
				}
				for range 2 {
					if err = m.Recover(ctx); err != nil {
						t.Fatal(err)
					}
				}
				db.Pool.Reset()
				p, err = emailpolicy.Read(ctx, db.Pool, false)
				if err != nil {
					t.Fatal(err)
				}
				want := "source"
				if rollback {
					want = "destination"
					if p.Enabled != enabled || snapshot() != before {
						t.Fatal("rollback changed exact prior policy/audit/effect/receipt")
					}
				} else if p.Enabled || p.LastEnableOperation != nil {
					t.Fatalf("restored policy not disabled %+v", p)
				}
				var sentinel string
				if err = db.Pool.QueryRow(ctx, `SELECT value FROM policy_sentinel`).Scan(&sentinel); err != nil || sentinel != want {
					t.Fatalf("database %s %v", sentinel, err)
				}
				bytes, err := os.ReadFile(file)
				if err != nil || string(bytes) != want {
					t.Fatalf("files %q %v", bytes, err)
				}
				var active int
				if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at>clock_timestamp()`).Scan(&active); err != nil {
					t.Fatal(err)
				}
				if rollback && active != 2 || !rollback && active != 0 {
					t.Fatalf("session count %d", active)
				}
			})
		}
	}
}

func TestUnknownEmailJournalVersion(t *testing.T) {
	m := storageManager(t)
	if err := m.writeJournal(journal{Phase: "capturing", Recovery: "retained", EmailPolicyVersion: 99}); err != nil {
		t.Fatal(err)
	}
	if err := m.Recover(context.Background()); err == nil {
		t.Fatal("unknown policy version accepted")
	}
	if _, err := os.Stat(m.journalPath()); err != nil {
		t.Fatal("unknown journal lost")
	}
}
