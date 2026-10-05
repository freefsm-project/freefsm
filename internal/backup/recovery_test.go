package backup

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func storageManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	c, e := instancecontrol.New(filepath.Join(root, "state"))
	if e != nil {
		t.Fatal(e)
	}
	m, e := New(Config{DSN: "postgres://unused/unused", UploadDir: filepath.Join(root, "uploads"), StateDir: c.StateDir(), Version: "v1.2.3", Commit: "abcdef1234567", Control: c})
	if e != nil {
		t.Fatal(e)
	}
	return m
}

func TestRecoveryOnlyStartsWithoutCreatingUploadRoot(t *testing.T) {
	root := t.TempDir()
	c, err := instancecontrol.New(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	upload := filepath.Join(root, "uploads")
	m, err := New(Config{DSN: "postgres://unused/unused", UploadDir: upload, StateDir: c.StateDir(), Version: "dev", Commit: "none", RecoveryOnly: true, Control: c})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Shutdown()
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(upload); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upload root created before recovery: %v", err)
	}
	if _, err := m.StartBackup(1, "source", "secret"); err == nil {
		t.Fatal("development transfer accepted")
	}
	if _, err := m.StageUpload(context.Background(), 1, strings.NewReader("archive"), "secret"); err == nil {
		t.Fatal("development upload accepted")
	}
}

func TestCOPYStreamsRowsLargerThanBuffer(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data.sql")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = io.WriteString(f, "COPY public.evidence (body) FROM stdin;\n"); e != nil {
		t.Fatal(e)
	}
	chunk := strings.Repeat("x", 65536)
	for i := 0; i < 512; i++ {
		if _, e = io.WriteString(f, chunk); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = io.WriteString(f, "\n\\.\n"); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	if e = importData(context.Background(), nil, p); e != nil {
		t.Fatal(e)
	}
	// A terminator-like fragment inside a long row must remain COPY data.
	r := bufio.NewReaderSize(strings.NewReader(strings.Repeat("x", 16)+"\\.\n\\.\n"), 16)
	cr := &copyReader{r: r, pathColumn: -1, columns: 1}
	b, e := io.ReadAll(cr)
	if e != nil || string(b) != strings.Repeat("x", 16)+"\\.\n" {
		t.Fatalf("fragment terminator: %q %v", b, e)
	}
}

func TestGenerationResetsEachProcessPools(t *testing.T) {
	m := storageManager(t)
	resets := 0
	m.cfg.ResetConnections = func() { resets++ }
	if e := m.RefreshConnections(); e != nil {
		t.Fatal(e)
	}
	if e := m.advanceGeneration(); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e := m.RefreshConnections(); e != nil {
			t.Fatal(e)
		}
	}
	if resets != 1 {
		t.Fatalf("resets=%d", resets)
	}
	if e := m.advanceGeneration(); e != nil {
		t.Fatal(e)
	}
	if e := m.RefreshConnections(); e != nil {
		t.Fatal(e)
	}
	if resets != 2 {
		t.Fatalf("resets=%d", resets)
	}
}

func TestRecoveryRetainsEvidenceAndAdmissionOnFailure(t *testing.T) {
	m := storageManager(t)
	ctx := context.Background()
	dir := filepath.Join(m.root, "needed-recovery")
	if e := privateDir(dir); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(dir, "database.dump"), []byte("remaining evidence"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := m.writeJournal(journal{Phase: "replacing", Recovery: "needed-recovery"}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e := m.Recover(ctx); e == nil {
			t.Fatal("recovery accepted incomplete evidence")
		}
		if _, _, e := m.cfg.Control.Enter(ctx); !errors.Is(e, instancecontrol.ErrMaintenance) {
			t.Fatalf("admission reopened: %v", e)
		}
		if _, e := os.Stat(filepath.Join(dir, "database.dump")); e != nil {
			t.Fatal("evidence deleted")
		}
	}
}

func TestRecoveryRejectsChangedDestination(t *testing.T) {
	m := storageManager(t)
	if e := m.writeJournal(journal{Phase: "capturing", Recovery: "safe"}); e != nil {
		t.Fatal(e)
	}
	m.cfg.DSN = "postgres://different/database"
	if e := m.Recover(context.Background()); e == nil {
		t.Fatal("changed destination accepted")
	}
	if _, e := os.Stat(m.journalPath()); e != nil {
		t.Fatal("journal removed")
	}
	if _, _, e := m.cfg.Control.Enter(context.Background()); !errors.Is(e, instancecontrol.ErrMaintenance) {
		t.Fatal("admission reopened")
	}
}

func TestCompetingManagerOperationRejected(t *testing.T) {
	m := storageManager(t)
	release, e := m.cfg.Control.OperationLock()
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if _, e = m.StartBackup(1, "source", "password"); !errors.Is(e, instancecontrol.ErrOperationBusy) {
		t.Fatalf("competing operation: %v", e)
	}
}

func TestStartupCleanupPreservesAnotherProcessTransfer(t *testing.T) {
	m := storageManager(t)
	j, release, e := m.newJob(1, "backup")
	if e != nil {
		t.Fatal(e)
	}
	j.active = false
	j.Phase = "complete"
	release()
	m.wg.Done()
	if e = os.WriteFile(filepath.Join(j.dir, "archive.age"), []byte("encrypted bytes"), 0600); e != nil {
		t.Fatal(e)
	}
	other, e := New(m.cfg)
	if e != nil {
		t.Fatal(e)
	}
	if e = other.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	reader, _, _, e := m.OpenDownload(1, j.ID)
	if e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	j.ExpiresAt = time.Now().Add(-time.Second)
	m.expireLocked()
	m.mu.Unlock()
	if _, _, _, e = m.OpenDownload(1, j.ID); e == nil {
		t.Fatal("expired transfer accepted new download")
	}
	b, e := io.ReadAll(reader)
	if e != nil || string(b) != "encrypted bytes" {
		t.Fatalf("existing transfer: %q %v", b, e)
	}
	if e = reader.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(j.dir); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("expired artifact retained: %v", e)
	}
	j, release, e = m.newJob(1, "upload")
	if e != nil {
		t.Fatal(e)
	}
	j.active = false
	release()
	m.wg.Done()
	m.Shutdown()
	if e = other.Recover(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(j.dir); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("abandoned transfer retained")
	}
}

func TestRecoveryPredestructiveAndCommittedCleanup(t *testing.T) {
	for _, phase := range []string{"capturing", "committed", "rolled-back"} {
		t.Run(phase, func(t *testing.T) {
			m := storageManager(t)
			if e := m.writeJournal(journal{Phase: phase, Recovery: "safe-cleanup"}); e != nil {
				t.Fatal(e)
			}
			if e := m.Recover(context.Background()); e != nil {
				t.Fatal(e)
			}
			_, release, e := m.cfg.Control.Enter(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			release()
			if e = m.Recover(context.Background()); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestAuthenticatedEOFAndWrongPassword(t *testing.T) {
	dir := t.TempDir()
	if e := os.WriteFile(filepath.Join(dir, "database.dump"), []byte("payload"), 0600); e != nil {
		t.Fatal(e)
	}
	items, e := inventory(dir)
	if e != nil {
		t.Fatal(e)
	}
	man := manifest{Format: 1, Source: "source", Version: "v1.2.3", Commit: "abcdef1234567", Captured: time.Now(), Payloads: items}
	if e = seal(dir, "correct password", man); e != nil {
		t.Fatal(e)
	}
	original, e := os.ReadFile(filepath.Join(dir, "archive.age"))
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"valid", "wrong-password", "truncated", "tampered"} {
		t.Run(kind, func(t *testing.T) {
			bytes := append([]byte(nil), original...)
			password := "correct password"
			switch kind {
			case "wrong-password":
				password = "incorrect password"
			case "truncated":
				bytes = bytes[:len(bytes)-1]
			case "tampered":
				bytes[len(bytes)-1] ^= 1
			}
			out := t.TempDir()
			if e := os.WriteFile(filepath.Join(out, "archive.age"), bytes, 0600); e != nil {
				t.Fatal(e)
			}
			_, e := unseal(out, password)
			if (e == nil) != (kind == "valid") {
				t.Fatalf("authentication result: %v", e)
			}
		})
	}
}

func TestArchiveBuildIdentityCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name            string
		kind            BuildKind
		version, commit string
		want            BuildKind
	}{
		{"legacy-release", "", "v1.2.3", "abcdef1234567", BuildRelease},
		{"legacy-unidentified-rejected", "", "dev", "none", ""},
		{"declared-release-invalid", BuildRelease, "dev", "none", ""},
		{"unknown-kind", "future", "v1.2.3", "abcdef1234567", ""},
		{"development-unidentified", BuildDevelopment, "", "", BuildDevelopment},
		{"development-dirty", BuildDevelopment, "v1.2.3-4-gabcdef-dirty", "abcdef1234567", BuildDevelopment},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "database.dump"), []byte("payload"), 0600); err != nil {
				t.Fatal(err)
			}
			items, err := inventory(dir)
			if err != nil {
				t.Fatal(err)
			}
			man := manifest{Format: 1, Source: "source", Captured: time.Now(), BuildKind: tc.kind, Version: tc.version, Commit: tc.commit, Payloads: items}
			if err := seal(dir, "password", man); err != nil {
				t.Fatal(err)
			}
			out := t.TempDir()
			b, err := os.ReadFile(filepath.Join(dir, "archive.age"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "archive.age"), b, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := unseal(out, "password")
			if tc.want == "" {
				if err == nil {
					t.Fatal("invalid identity accepted")
				}
			} else if err != nil || got.BuildKind != tc.want {
				t.Fatalf("identity: %+v, %v", got, err)
			}
		})
	}
}
