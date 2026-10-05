package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const crashArchivePassword = "disposable-engine-crash-test-password"

// Environment handling and process barriers exist only in this test binary.
// Production binaries have neither this entry point nor an environment failpoint.
type crashChildSpec struct{ DSN, UploadDir, StateDir, Archive, Mode, Checkpoint, Ready string }

func TestBackupCrashChild(t *testing.T) {
	path := os.Getenv("FREEFSM_BACKUP_CRASH_CHILD_SPEC")
	if path == "" {
		t.Skip("subprocess helper")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var spec crashChildSpec
	if e = json.Unmarshal(b, &spec); e != nil {
		t.Fatal(e)
	}
	c, e := instancecontrol.New(spec.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	cfg := Config{DSN: spec.DSN, UploadDir: spec.UploadDir, StateDir: spec.StateDir, Version: "v1.2.3", Commit: "abcdef1234567", Control: c, ResetConnections: func() {}}
	if spec.Mode == "recover" {
		cfg.BuildKind = BuildDevelopment
		cfg.Version = "dev"
		cfg.Commit = "none"
	}
	m, e := New(cfg)
	if e != nil {
		t.Fatal(e)
	}
	m.checkpoint = func(point string) error {
		if point == spec.Checkpoint {
			if e := atomicJSON(spec.Ready, point); e != nil {
				return e
			}
			select {}
		}
		return nil
	}
	if spec.Mode == "recover" {
		if spec.Archive != "" {
			t.Fatal("recovery must not receive an archive")
		}
		if e = m.Recover(context.Background()); e != nil {
			t.Fatal(e)
		}
		m.Shutdown()
		return
	}
	if e = m.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(spec.Archive)
	if e != nil {
		t.Fatal(e)
	}
	op, e := m.StageUpload(context.Background(), 1, f, crashArchivePassword)
	_ = f.Close()
	if e != nil {
		t.Fatal(e)
	}
	op = awaitOperation(t, m, 1, op.ID)
	if op.Phase != "ready" {
		t.Fatalf("stage: %+v", op)
	}
	op, e = m.StartRestore(context.Background(), 1, op.ID, "destination", "destination")
	if e != nil {
		t.Fatal(e)
	}
	op = awaitOperation(t, m, 1, op.ID)
	t.Fatalf("restore unexpectedly reached terminal phase instead of crash barrier: %+v", op)
}

func awaitOperation(t *testing.T, m *Manager, actor int64, id string) Operation {
	t.Helper()
	deadline := time.NewTimer(90 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		op, e := m.Status(actor, id)
		if e != nil {
			t.Fatal(e)
		}
		if op.Phase == "ready" || op.Phase == "complete" || op.Phase == "failed" {
			m.wg.Wait() // terminal status is published immediately before releasing the flock
			return op
		}
		select {
		case <-deadline.C:
			t.Fatalf("operation timed out: %+v", op)
		case <-ticker.C:
		}
	}
}

type crashFixture struct {
	admin    *pgx.Conn
	adminURL string
}

func newCrashFixture(t *testing.T) *crashFixture {
	t.Helper()
	url := os.Getenv("FREEFSM_BACKUP_TEST_ADMIN_URL")
	if url == "" {
		t.Skip("FREEFSM_BACKUP_TEST_ADMIN_URL must identify a disposable cluster")
	}
	c, e := pgx.Connect(context.Background(), url)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return &crashFixture{c, url}
}
func (f *crashFixture) manager(t *testing.T, label string) *Manager {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("crash_%d", time.Now().UnixNano())
	ident := pgx.Identifier{name}.Sanitize()
	if _, e := f.admin.Exec(ctx, "CREATE ROLE "+ident+" LOGIN NOCREATEDB NOSUPERUSER"); e != nil {
		t.Fatal(e)
	}
	if _, e := f.admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER "+ident+" TEMPLATE template0 ENCODING 'UTF8'"); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := f.admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
		if _, e := f.admin.Exec(ctx, "DROP ROLE "+ident); e != nil {
			t.Error(e)
		}
	})
	parsed, e := pgx.ParseConfig(f.adminURL)
	if e != nil {
		t.Fatal(e)
	}
	dsn := fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=disable", name, parsed.Host, parsed.Port, name)
	root := t.TempDir()
	c, e := instancecontrol.New(filepath.Join(root, "state"))
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(Config{DSN: dsn, StateDir: c.StateDir(), UploadDir: filepath.Join(root, "uploads"), Version: "v1.2.3", Commit: "abcdef1234567", Control: c, ResetConnections: func() {}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(m.Shutdown)
	if e = m.Recover(ctx); e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(m.cfg.UploadDir, 0700); e != nil {
		t.Fatal(e)
	}
	db, e := database.Connect(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	if e = db.Migrate(ctx, os.DirFS("../database/migrations")); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Pool.Exec(ctx, `CREATE TABLE public.engine_crash_probe (id bigserial PRIMARY KEY, value text NOT NULL)`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Pool.Exec(ctx, "INSERT INTO public.engine_crash_probe(value) VALUES($1)", label); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"a.bin", "z.bin"} {
		if e = os.WriteFile(filepath.Join(m.cfg.UploadDir, name), []byte(label+":"+name), 0600); e != nil {
			t.Fatal(e)
		}
	}
	return m
}
func createCrashArchive(t *testing.T, m *Manager) string {
	t.Helper()
	op, e := m.StartBackup(1, "source", crashArchivePassword)
	if e != nil {
		t.Fatal(e)
	}
	op = awaitOperation(t, m, 1, op.ID)
	if op.Phase != "complete" {
		t.Fatalf("backup: %+v", op)
	}
	r, _, _, e := m.OpenDownload(1, op.ID)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	p := filepath.Join(t.TempDir(), "archive.age")
	f, e := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = io.Copy(f, r); e != nil {
		t.Fatal(e)
	}
	if e = f.Sync(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	return p
}
func stageCrashArchive(t *testing.T, m *Manager, p string) Operation {
	t.Helper()
	f, e := os.Open(p)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	op, e := m.StageUpload(context.Background(), 1, f, crashArchivePassword)
	if e != nil {
		t.Fatal(e)
	}
	op = awaitOperation(t, m, 1, op.ID)
	if op.Phase != "ready" {
		t.Fatalf("stage: %+v", op)
	}
	return op
}

func runCrashChild(t *testing.T, m *Manager, archive, mode, checkpoint string) {
	t.Helper()
	dir := t.TempDir()
	spec := crashChildSpec{DSN: m.cfg.DSN, UploadDir: m.cfg.UploadDir, StateDir: m.cfg.StateDir, Archive: archive, Mode: mode, Checkpoint: checkpoint, Ready: filepath.Join(dir, "ready.json")}
	path := filepath.Join(dir, "child.json")
	if e := atomicJSON(path, spec); e != nil {
		t.Fatal(e)
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(executable, "-test.run=^TestBackupCrashChild$", "-test.timeout=100s")
	cmd.Env = append(os.Environ(), "FREEFSM_BACKUP_CRASH_CHILD_SPEC="+path)
	var output bytes.Buffer
	cmd.Stdout = &boundedWriter{w: &output, remaining: 64 << 10}
	cmd.Stderr = cmd.Stdout
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	defer func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	deadline := time.NewTimer(95 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if checkpoint != "" {
			if b, e := os.ReadFile(spec.Ready); e == nil {
				var reached string
				if e = json.Unmarshal(b, &reached); e != nil || reached != checkpoint {
					t.Fatalf("invalid child barrier: %q %v", b, e)
				}
				if e = cmd.Process.Kill(); e != nil {
					t.Fatal(e)
				}
				e = <-done
				stopped = true
				var exit *exec.ExitError
				if !errors.As(e, &exit) {
					t.Fatalf("expected real process termination, got %v", e)
				}
				status, ok := exit.Sys().(syscall.WaitStatus)
				if !ok || status.Signal() != syscall.SIGKILL {
					t.Fatalf("child not SIGKILLed: %v", e)
				}
				return
			}
		}
		select {
		case e = <-done:
			stopped = true
			if checkpoint != "" || e != nil {
				t.Fatalf("child exited before expected checkpoint %q: %v\n%s", checkpoint, e, output.String())
			}
			return
		case <-deadline.C:
			t.Fatalf("child did not reach %q\n%s", checkpoint, output.String())
		case <-ticker.C:
		}
	}
}

func recoveryEvidence(t *testing.T, m *Manager) []byte {
	t.Helper()
	b, e := os.ReadFile(m.journalPath())
	if e != nil {
		t.Fatal(e)
	}
	var j journal
	if e = json.Unmarshal(b, &j); e != nil || j.Phase != "replacing" {
		t.Fatalf("journal: %s %v", b, e)
	}
	if e = mVerifyRecovery(filepath.Join(m.root, j.Recovery)); e != nil {
		t.Fatal(e)
	}
	if _, release, e := m.cfg.Control.Enter(context.Background()); !errors.Is(e, instancecontrol.ErrMaintenance) {
		if release != nil {
			release()
		}
		t.Fatalf("admission reopened with incomplete recovery: %v", e)
	}
	return b
}
func assertCrashState(t *testing.T, m *Manager, label string) {
	t.Helper()
	ctx := context.Background()
	c, e := m.connect(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close(ctx)
	var value string
	if e = c.QueryRow(ctx, "SELECT value FROM public.engine_crash_probe").Scan(&value); e != nil || value != label {
		t.Fatalf("database state=%q want=%q err=%v", value, label, e)
	}
	entries, e := os.ReadDir(m.cfg.UploadDir)
	if e != nil || len(entries) != 2 {
		t.Fatalf("files: %v %v", entries, e)
	}
	for _, name := range []string{"a.bin", "z.bin"} {
		b, e := os.ReadFile(filepath.Join(m.cfg.UploadDir, name))
		if e != nil || string(b) != label+":"+name {
			t.Fatalf("file %s=%q want=%s err=%v", name, b, label, e)
		}
	}
}

func TestProcessCrashAndRepeatedInterruptedRollback(t *testing.T) {
	f := newCrashFixture(t)
	src := f.manager(t, "source")
	archive := createCrashArchive(t, src)
	for _, checkpoint := range []string{"replacing-durable", "database-dropped", "files-entry-removed", "protections-applied"} {
		t.Run(checkpoint, func(t *testing.T) {
			dst := f.manager(t, "destination")
			ctx := context.Background()
			if checkpoint == "database-dropped" {
				if e := dst.cfg.Control.DisableEmail(); e != nil {
					t.Fatal(e)
				}
			}
			priorEmail := dst.cfg.Control.EmailDisabled()
			runCrashChild(t, dst, archive, "restore", checkpoint)
			original := recoveryEvidence(t, dst)
			// Establish that this was an actual partial native restore, not just a
			// fabricated journal. The child is dead; inspect from a fresh connection.
			c, e := dst.connect(ctx)
			if e != nil {
				t.Fatal(e)
			}
			var exists bool
			if e = c.QueryRow(ctx, "SELECT to_regclass('public.engine_crash_probe') IS NOT NULL").Scan(&exists); e != nil {
				t.Fatal(e)
			}
			if checkpoint == "database-dropped" && exists {
				t.Fatal("child did not durably drop the schema")
			}
			if checkpoint == "files-entry-removed" {
				var value string
				if e = c.QueryRow(ctx, "SELECT value FROM public.engine_crash_probe").Scan(&value); e != nil || value != "source" {
					t.Fatalf("database not replaced: %q %v", value, e)
				}
				entries, e := os.ReadDir(dst.cfg.UploadDir)
				if e != nil || len(entries) != 1 || entries[0].Name() != "z.bin" {
					t.Fatalf("files not partially replaced: %v %v", entries, e)
				}
			}
			_ = c.Close(ctx)
			// Each rollback attempt is a NEW process using RecoveryOnly with dev
			// identity and no archive path/password. Kill it twice, at different phases.
			for _, point := range []string{"database-pre-data", "files-entry-removed"} {
				runCrashChild(t, dst, "", "recover", point)
				if b := recoveryEvidence(t, dst); !bytes.Equal(b, original) {
					t.Fatal("interrupted rollback replaced original journal")
				}
				c, e := dst.connect(ctx)
				if e != nil {
					t.Fatal(e)
				}
				if point == "database-pre-data" {
					var count int
					if e = c.QueryRow(ctx, "SELECT count(*) FROM public.engine_crash_probe").Scan(&count); e != nil || count != 0 {
						t.Fatalf("rollback database was not partially rebuilt: count=%d err=%v", count, e)
					}
				} else {
					var value string
					if e = c.QueryRow(ctx, "SELECT value FROM public.engine_crash_probe").Scan(&value); e != nil || value != "destination" {
						t.Fatalf("rollback database not restored: %q %v", value, e)
					}
					entries, e := os.ReadDir(dst.cfg.UploadDir)
					if e != nil || len(entries) >= 2 {
						t.Fatalf("rollback files were not interrupted: %v %v", entries, e)
					}
				}
				_ = c.Close(ctx)
			}
			runCrashChild(t, dst, "", "recover", "")
			assertCrashState(t, dst, "destination")
			if dst.cfg.Control.EmailDisabled() != priorEmail {
				t.Fatal("rollback failed to restore prior email gate")
			}
			if _, e = os.Stat(dst.journalPath()); !errors.Is(e, os.ErrNotExist) {
				t.Fatal("resolved journal retained")
			}
			_, release, e := dst.cfg.Control.Enter(ctx)
			if e != nil {
				t.Fatal(e)
			}
			release()
			// Yet another real startup is idempotent after cleanup.
			runCrashChild(t, dst, "", "recover", "")
			assertCrashState(t, dst, "destination")
		})
	}
}

// These tests exercise the OTHER durable decision: once committed is fsynced,
// every subsequent startup must keep the replacement, even with an unusable,
// partially removed recovery copy. No journal is fabricated by the parent.
func TestProcessCrashAfterCommitAndDuringCleanup(t *testing.T) {
	f := newCrashFixture(t)
	src := f.manager(t, "source")
	seedCrashSessions(t, src, "source")
	archive := createCrashArchive(t, src)
	for _, point := range []string{"committed-durable", "recovery-entry-removed"} {
		t.Run(point, func(t *testing.T) {
			dst := f.manager(t, "destination")
			seedCrashSessions(t, dst, "destination")
			if dst.cfg.Control.EmailDisabled() {
				t.Fatal("fixture must start with email enabled")
			}
			runCrashChild(t, dst, archive, "restore", point)
			journalBytes, err := os.ReadFile(dst.journalPath())
			if err != nil {
				t.Fatal(err)
			}
			var decision journal
			if err = json.Unmarshal(journalBytes, &decision); err != nil || decision.Phase != "committed" {
				t.Fatalf("restore did not durably commit: %s %v", journalBytes, err)
			}
			recoveryDir := filepath.Join(dst.root, decision.Recovery)
			if point == "committed-durable" {
				if err = mVerifyRecovery(recoveryDir); err != nil {
					t.Fatalf("recovery copy removed before committed barrier: %v", err)
				}
			} else if err = mVerifyRecovery(recoveryDir); err == nil {
				t.Fatal("cleanup barrier did not interrupt recovery-copy removal")
			}
			assertCommittedCrashState(t, dst)
			assertCrashMaintenance(t, dst)
			entries, err := os.ReadDir(recoveryDir)
			if err != nil {
				t.Fatal(err)
			}
			remaining := len(entries)
			for attempt := 0; attempt < 2; attempt++ {
				runCrashChild(t, dst, "", "recover", "recovery-entry-removed")
				next, err := os.ReadDir(recoveryDir)
				if err != nil || len(next) != remaining-1 || len(next) == 0 {
					t.Fatalf("cleanup did not make partial durable progress: before=%d after=%d err=%v", remaining, len(next), err)
				}
				remaining = len(next)
				current, err := os.ReadFile(dst.journalPath())
				if err != nil || !bytes.Equal(current, journalBytes) {
					t.Fatalf("cleanup changed committed decision: %s %v", current, err)
				}
				assertCommittedCrashState(t, dst)
				assertCrashMaintenance(t, dst)
			}
			for attempt := 0; attempt < 2; attempt++ {
				runCrashChild(t, dst, "", "recover", "")
				assertCommittedCrashState(t, dst)
				for _, p := range []string{recoveryDir, dst.journalPath()} {
					if _, err = os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("safe cleanup did not finish for %s: %v", filepath.Base(p), err)
					}
				}
				_, release, err := dst.cfg.Control.Enter(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				release()
			}
		})
	}
}

func seedCrashSessions(t *testing.T, m *Manager, label string) {
	t.Helper()
	ctx := context.Background()
	c, err := m.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var userID int64
	err = c.QueryRow(ctx, `INSERT INTO public.users(email,password_hash,name,role,company_id)
		SELECT $1,'fixture-only-not-a-password-hash',$2,'admin',id FROM public.companies ORDER BY id LIMIT 1 RETURNING id`, label+"@crash.invalid", label).Scan(&userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"web", "mobile"} {
		if _, err = c.Exec(ctx, `INSERT INTO public.sessions(token_hash,user_id,kind,expires_at) VALUES($1,$2,$3,NOW()+INTERVAL '1 day')`, label+"-"+kind, userID, kind); err != nil {
			t.Fatal(err)
		}
	}
	var active int
	if err = c.QueryRow(ctx, `SELECT count(*) FROM public.sessions WHERE expires_at>NOW() AND revoked_at IS NULL`).Scan(&active); err != nil || active != 2 {
		t.Fatalf("session fixture not active: %d %v", active, err)
	}
}

func assertCommittedCrashState(t *testing.T, m *Manager) {
	t.Helper()
	assertCrashState(t, m, "source")
	ctx := context.Background()
	c, err := m.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	var total, protected, sourceWeb, sourceMobile int
	err = c.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE expires_at<NOW() AND revoked_at IS NOT NULL),
		count(*) FILTER (WHERE token_hash='source-web' AND kind='web'),count(*) FILTER (WHERE token_hash='source-mobile' AND kind='mobile') FROM public.sessions`).Scan(&total, &protected, &sourceWeb, &sourceMobile)
	if err != nil || total != 2 || protected != 2 || sourceWeb != 1 || sourceMobile != 1 {
		t.Fatalf("committed session protections lost: total=%d protected=%d web=%d mobile=%d err=%v", total, protected, sourceWeb, sourceMobile, err)
	}
	if !m.cfg.Control.EmailDisabled() {
		t.Fatal("committed email gate reopened")
	}
}

func assertCrashMaintenance(t *testing.T, m *Manager) {
	t.Helper()
	_, release, err := m.cfg.Control.Enter(context.Background())
	if release != nil {
		release()
	}
	if !errors.Is(err, instancecontrol.ErrMaintenance) {
		t.Fatalf("unfinished cleanup reopened admission: %v", err)
	}
}

func TestAsyncFailuresAreActionableAndSanitized(t *testing.T) {
	f := newCrashFixture(t)
	m := f.manager(t, "source")
	archive := createCrashArchive(t, m)
	check := func(t *testing.T, m *Manager, op Operation, want FailureCategory) {
		t.Helper()
		op = awaitOperation(t, m, 1, op.ID)
		if op.Phase != "failed" || op.Failure.Category != want || op.Error != failureMessages[want] || op.Failure.DiagnosticID == "" {
			t.Fatalf("failure: %+v, want %s", op, want)
		}
		b, e := os.ReadFile(filepath.Join(m.root, "last-failure.json"))
		if e != nil {
			t.Fatal(e)
		}
		var d Diagnostic
		if e = json.Unmarshal(b, &d); e != nil {
			t.Fatal(e)
		}
		if d.Failure != op.Failure || len(b) > 8192 || strings.Contains(string(b), m.cfg.DSN) || strings.Contains(string(b), crashArchivePassword) || strings.Contains(string(b), op.ID) {
			t.Fatalf("diagnostic unsafe/unmatched: %s", b)
		}
	}
	t.Run("wrong-password", func(t *testing.T) {
		r, e := os.Open(archive)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Close()
		op, e := m.StageUpload(context.Background(), 1, r, "incorrect-password-not-for-logs")
		if e != nil {
			t.Fatal(e)
		}
		check(t, m, op, FailureArchiveAuthentication)
	})
	t.Run("release-mismatch", func(t *testing.T) {
		cfg := m.cfg
		cfg.Version = "v9.9.9"
		other, e := New(cfg)
		if e != nil {
			t.Fatal(e)
		}
		defer other.Shutdown()
		r, e := os.Open(archive)
		if e != nil {
			t.Fatal(e)
		}
		defer r.Close()
		op, e := other.StageUpload(context.Background(), 1, r, crashArchivePassword)
		if e != nil {
			t.Fatal(e)
		}
		check(t, other, op, FailureReleaseMismatch)
	})
	t.Run("native-invalid-dump", func(t *testing.T) {
		dir := t.TempDir()
		if e := os.WriteFile(filepath.Join(dir, "database.dump"), []byte("not-a-native-postgresql-dump"), 0600); e != nil {
			t.Fatal(e)
		}
		items, e := inventory(dir)
		if e != nil {
			t.Fatal(e)
		}
		man := manifest{Format: 1, Source: "source", Version: m.cfg.Version, Commit: m.cfg.Commit, Captured: time.Now().UTC(), Payloads: items}
		if e = seal(dir, crashArchivePassword, man); e != nil {
			t.Fatal(e)
		}
		r, e := os.Open(filepath.Join(dir, "archive.age"))
		if e != nil {
			t.Fatal(e)
		}
		defer r.Close()
		op, e := m.StageUpload(context.Background(), 1, r, crashArchivePassword)
		if e != nil {
			t.Fatal(e)
		}
		check(t, m, op, FailureArchiveFormat)
		b, e := os.ReadFile(filepath.Join(m.root, "last-failure.json"))
		if e != nil {
			t.Fatal(e)
		}
		var d Diagnostic
		if e = json.Unmarshal(b, &d); e != nil {
			t.Fatal(e)
		}
		if len(d.Causes) != 1 || d.Causes[0].Tool != "pg_restore" || d.Causes[0].ExitCode == 0 || len(d.Causes[0].Signals) == 0 {
			t.Fatalf("native command diagnostics lost: %+v", d)
		}
	})
	t.Run("missing-tools", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		op, e := m.StartBackup(1, "source", crashArchivePassword)
		if e != nil {
			t.Fatal(e)
		}
		check(t, m, op, FailureToolUnavailable)
	})
}

func TestStagedRestoreCannotOverwriteFailedRecovery(t *testing.T) {
	f := newCrashFixture(t)
	src := f.manager(t, "source")
	dst := f.manager(t, "destination")
	archive := createCrashArchive(t, src)
	a := stageCrashArchive(t, dst, archive)
	b := stageCrashArchive(t, dst, archive)
	const secret = "DO_NOT_LOG_COPY_ROW_OR_DSN"
	dst.checkpoint = func(point string) error {
		switch point {
		case "files-entry-removed":
			return &pgconn.PgError{Code: "23514", Message: secret, Detail: secret}
		case "rollback-start":
			return &os.PathError{Op: "open", Path: secret, Err: syscall.EACCES}
		}
		return nil
	}
	if _, e := dst.StartRestore(context.Background(), 1, a.ID, "destination", "destination"); e != nil {
		t.Fatal(e)
	}
	a = awaitOperation(t, dst, 1, a.ID)
	dst.wg.Wait()
	if a.Phase != "failed" || a.Failure.Category != FailureRecoveryRequired {
		t.Fatalf("first restore failure: %+v", a)
	}
	journalBefore := recoveryEvidence(t, dst)
	// public is valid and fully restored, so the old StartRestore implementation
	// would pass preflight and overwrite A's journal while files are still mixed.
	c, e := dst.connect(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	var value string
	e = c.QueryRow(context.Background(), "SELECT value FROM public.engine_crash_probe").Scan(&value)
	_ = c.Close(context.Background())
	if e != nil || value != "source" {
		t.Fatalf("first restore did not reach mixed state: %q %v", value, e)
	}
	remaining, e := os.ReadFile(filepath.Join(dst.cfg.UploadDir, "z.bin"))
	if e != nil || string(remaining) != "destination:z.bin" {
		t.Fatalf("expected destination file: %q %v", remaining, e)
	}
	if _, e = os.Stat(filepath.Join(dst.cfg.UploadDir, "a.bin")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("expected partial file replacement")
	}
	if _, e := dst.StartRestore(context.Background(), 1, b.ID, "destination", "destination"); !errors.Is(e, ErrRecoveryRequired) {
		t.Fatalf("second restore did not reject outstanding recovery: %v", e)
	}
	if got, e := dst.Status(1, b.ID); e != nil || got != b {
		t.Fatalf("second staged operation changed: %+v %v", got, e)
	}
	if after := recoveryEvidence(t, dst); !bytes.Equal(after, journalBefore) {
		t.Fatal("second restore overwrote recovery decision")
	}
	report, e := os.ReadFile(filepath.Join(dst.root, "last-failure.json"))
	if e != nil {
		t.Fatal(e)
	}
	var diagnostic Diagnostic
	if e = json.Unmarshal(report, &diagnostic); e != nil {
		t.Fatal(e)
	}
	if len(diagnostic.Causes) != 2 || diagnostic.Causes[0].SQLState != "23514" || diagnostic.Causes[1].Category != FailureStorage || strings.Contains(string(report), secret) {
		t.Fatalf("lost/unsafe failure causes: %s", report)
	}
	dst.checkpoint = nil
	if e = dst.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	assertCrashState(t, dst, "destination")
	if _, e = dst.StartRestore(context.Background(), 1, b.ID, "destination", "destination"); e != nil {
		t.Fatal(e)
	}
	b = awaitOperation(t, dst, 1, b.ID)
	if b.Phase != "complete" {
		t.Fatalf("restore after recovery: %+v", b)
	}
	assertCrashState(t, dst, "source")
}
