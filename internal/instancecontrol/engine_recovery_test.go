package instancecontrol_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/backup"
	"github.com/freefsm-project/freefsm/internal/database"
	"github.com/freefsm-project/freefsm/internal/emailpolicy"
	"github.com/freefsm-project/freefsm/internal/handlers"
	"github.com/freefsm-project/freefsm/internal/instancecontrol"
	"github.com/freefsm-project/freefsm/internal/testutil"
	"github.com/jackc/pgx/v5"
)

const enginePassword = "engine-release-qualification-password"

func engineManager(t *testing.T, cfg backup.Config) *backup.Manager {
	t.Helper()
	c, err := instancecontrol.New(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Control = c
	cfg.ResetConnections = func() {}
	m, err := backup.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Shutdown)
	return m
}

func engineInstance(t *testing.T, label string) (backup.Config, *backup.Manager) {
	t.Helper()
	root := t.TempDir()
	cfg := backup.Config{DSN: testutil.InstanceDatabase(t), StateDir: filepath.Join(root, "state"), UploadDir: filepath.Join(root, "uploads"), BuildKind: backup.BuildDevelopment}
	m := engineManager(t, cfg)
	s, err := m.BeginStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	db, err := database.Connect(context.Background(), cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.Migrate(context.Background(), database.MigrationFS()); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	control, err := instancecontrol.New(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = emailpolicy.Import(context.Background(), tx, control.LegacyEmailDisabled); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(context.Background(), "CREATE TABLE release_probe(value text NOT NULL); INSERT INTO release_probe VALUES ('"+label+"')"); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(cfg.UploadDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cfg.UploadDir, "sentinel"), []byte(label), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	return cfg, m
}

func engineAwait(t *testing.T, m *backup.Manager, id string) backup.Operation {
	t.Helper()
	deadline := time.After(90 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		op, err := m.StatusCapability(id)
		if err != nil {
			t.Fatal(err)
		}
		switch op.Phase {
		case "ready", "complete", "failed", "recovery-required":
			return op
		}
		select {
		case <-deadline:
			t.Fatalf("operation timeout: %+v", op)
		case <-tick.C:
		}
	}
}

type engineReleaseSpec struct {
	DSN, StateDir, UploadDir, Archive, Result, Mode string
}

func TestEngineReleaseChild(t *testing.T) {
	path := os.Getenv("FREEFSM_ENGINE_RELEASE_SPEC")
	if path == "" {
		t.Skip("subprocess helper")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var spec engineReleaseSpec
	if err = json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	c, err := instancecontrol.New(spec.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := backup.New(backup.Config{DSN: spec.DSN, StateDir: spec.StateDir, UploadDir: spec.UploadDir, BuildKind: backup.BuildDevelopment, Control: c, ResetConnections: func() {}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	if spec.Mode == "drain" {
		instancecontrol.SetAdmissionCheckpointForTest(c, func(point string) {
			if point == "maintenance-closed" {
				if err := os.WriteFile(spec.Result+".closed", []byte("closed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		})
		s, err := m.BeginStartup(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if err = os.WriteFile(spec.Result, []byte("initializing"), 0600); err != nil {
			t.Fatal(err)
		}
		if err = s.Finish(context.Background()); err != nil {
			t.Fatal(err)
		}
		return
	}
	var failed atomic.Bool
	instancecontrol.SetAdmissionFaultForTest(c, func(point string) error {
		if point == "admission-directory-sync" {
			b, err := os.ReadFile(filepath.Join(spec.StateDir, "maintenance"))
			if err != nil {
				return err
			}
			if strings.Contains(string(b), `"Decision":"verified-open"`) {
				// The real restore still owns exclusive intent/admission while
				// publication is paused at its final directory-sync boundary.
				results := make(chan error, 8)
				for range 8 {
					go func() {
						_, release, err := c.Enter(context.Background())
						if release != nil {
							release()
						}
						results <- err
					}()
				}
				for range 8 {
					if err := <-results; !errors.Is(err, instancecontrol.ErrMaintenance) {
						return errors.New("reader crossed engine release publication")
					}
				}
				failed.Store(true)
				return syscall.EIO
			}
		}
		if failed.Load() && point == "admission-read-directory-sync" {
			return syscall.EIO
		}
		return nil
	})
	if spec.Mode == "restore" {
		f, err := os.Open(spec.Archive)
		if err != nil {
			t.Fatal(err)
		}
		op, err := m.StageUpload(context.Background(), 1, f, enginePassword)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if op = engineAwait(t, m, op.ID); op.Phase != "ready" {
			t.Fatalf("stage: %+v", op)
		}
		if _, err = m.StartRestore(context.Background(), 1, op.ID, "destination", "destination"); err != nil {
			t.Fatal(err)
		}
		op = engineAwait(t, m, op.ID)
		m.Shutdown()
		if !failed.Load() || op.Phase != "recovery-required" || op.Failure.DiagnosticID == "" {
			t.Fatalf("final sync failure falsely completed: %+v reached=%v", op, failed.Load())
		}
		b, err = json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(spec.Result, b, 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err = os.ReadFile(spec.Result)
	if err != nil {
		t.Fatal(err)
	}
	var original backup.Operation
	if err = json.Unmarshal(b, &original); err != nil {
		t.Fatal(err)
	}
	failed.Store(true)
	routedStatus := func() backup.Operation {
		t.Helper()
		router := handlers.NewBackupRouter(nil, m, c, nil, nil, nil, "", nil)
		shell := httptest.NewRecorder()
		router.ServeHTTP(shell, httptest.NewRequest(http.MethodGet, "/settings/backup/status-page", nil))
		match := regexp.MustCompile(`data-csrf="([^"]+)"`).FindStringSubmatch(shell.Body.String())
		if shell.Code != http.StatusOK || len(match) != 2 {
			t.Fatalf("status shell: %d %s", shell.Code, shell.Body)
		}
		r := httptest.NewRequest(http.MethodPost, "/settings/backup/status", nil)
		r.Header.Set("X-CSRF-Token", match[1])
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		r.Header.Set("X-Backup-Operation", original.ID)
		for _, cookie := range shell.Result().Cookies() {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		var op backup.Operation
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &op) != nil || !op.Reconstructed {
			t.Fatalf("capability status: %d %s", w.Code, w.Body)
		}
		return op
	}
	for range 3 {
		_, release, err := c.Enter(context.Background())
		if release != nil {
			release()
		}
		if !errors.Is(err, instancecontrol.ErrMaintenance) {
			t.Fatalf("persistent EIO admitted after owner exit: %v", err)
		}
		op := routedStatus()
		if op.Phase == "complete" || op.Failure.DiagnosticID != original.Failure.DiagnosticID {
			t.Fatalf("persistent EIO status/history: %+v", op)
		}
	}
	failed.Store(false) // only storage repair, no Recover or fabricated entitlement
	op := routedStatus()
	if op.Phase != "complete" || op.Release != "verified-open" || op.Failure.DiagnosticID != original.Failure.DiagnosticID {
		t.Fatalf("same-operation repaired release/history: %+v", op)
	}
	_, release, err := c.Enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestEngineStartupCrossProcessNestedDrain(t *testing.T) {
	cfg, _ := engineInstance(t, "destination")
	c, err := instancecontrol.New(cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Control = c
	m, err := backup.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	ctx, release, err := c.Enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	spec := engineReleaseSpec{DSN: cfg.DSN, StateDir: cfg.StateDir, UploadDir: cfg.UploadDir, Result: filepath.Join(t.TempDir(), "result"), Mode: "drain"}
	b, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "spec.json")
	if err = os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	childContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(childContext, executable, "-test.run=^TestEngineReleaseChild$", "-test.timeout=25s")
	cmd.Env = append(os.Environ(), "FREEFSM_ENGINE_RELEASE_SPEC="+path)
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		cancel()
		if done != nil {
			<-done
		}
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err = os.Stat(spec.Result + ".closed"); err == nil {
			break
		}
		select {
		case err := <-done:
			done = nil
			t.Fatalf("startup exited before closing: %v", err)
		case <-childContext.Done():
			t.Fatal("startup never closed admission")
		case <-tick.C:
		}
	}
	_, freshRelease, err := c.Enter(context.Background())
	if freshRelease != nil {
		freshRelease()
	}
	if !errors.Is(err, instancecontrol.ErrMaintenance) {
		t.Fatalf("fresh reader crossed closed transition: %v", err)
	}
	_, nestedRelease, err := c.Enter(ctx)
	if err != nil {
		t.Fatalf("nested drain deadlocked/denied: %v", err)
	}
	nestedRelease()
	if _, err = os.Stat(spec.Result); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("startup initialized before drain: %v", err)
	}
	if unlock, err := c.OperationLock(); !errors.Is(err, instancecontrol.ErrOperationBusy) {
		if unlock != nil {
			unlock()
		}
		t.Fatalf("startup lost exclusive operation ownership: %v", err)
	}
	release()
	err = <-done
	done = nil
	if err != nil {
		t.Fatalf("startup failed after drain: %v", err)
	}
	_, freshRelease, err = c.Enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	freshRelease()
}

func TestEngineReleaseFinalSyncAcrossProcesses(t *testing.T) {
	_, src := engineInstance(t, "source")
	op, err := src.StartBackup(1, "source", enginePassword)
	if err != nil {
		t.Fatal(err)
	}
	if op = engineAwait(t, src, op.ID); op.Phase != "complete" {
		t.Fatalf("backup: %+v", op)
	}
	r, _, _, err := src.OpenDownload(1, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	archive := filepath.Join(t.TempDir(), "archive.age")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(f, r); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, dst := engineInstance(t, "destination")
	dst.Shutdown()
	spec := engineReleaseSpec{DSN: cfg.DSN, StateDir: cfg.StateDir, UploadDir: cfg.UploadDir, Archive: archive, Result: filepath.Join(t.TempDir(), "result.json")}
	path := filepath.Join(t.TempDir(), "spec.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"restore", "read"} {
		spec.Mode = mode
		b, err := json.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		cmd := exec.CommandContext(ctx, executable, "-test.run=^TestEngineReleaseChild$", "-test.timeout=95s")
		cmd.Env = append(os.Environ(), "FREEFSM_ENGINE_RELEASE_SPEC="+path)
		out, err := cmd.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("%s child: %v\n%s", mode, err, out)
		}
	}
	c, err := pgx.Connect(context.Background(), cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(context.Background())
	var value string
	if err = c.QueryRow(context.Background(), "SELECT value FROM release_probe").Scan(&value); err != nil || value != "source" {
		t.Fatalf("wrong database generation: %q %v", value, err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.UploadDir, "sentinel"))
	if err != nil || string(b) != "source" {
		t.Fatalf("wrong file generation: %q %v", b, err)
	}
}
