package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These exercise the production cleanup/status adapters with synthetic journal
// input. They are not whole-engine DB/files generation or crash qualification.
func TestResolutionSelectionPrecedesCleanup(t *testing.T) {
	for _, phase := range []string{"capturing", "committed", "rolled-back"} {
		t.Run(phase, func(t *testing.T) {
			m, r := resolutionFixture(t)
			j := journal{Phase: phase, Recovery: strings.Repeat("ab", 32) + "-recovery", Destination: m.destinationIdentity(), UploadDir: m.cfg.UploadDir}
			if phase != "capturing" {
				if err := atomicJSON(filepath.Join(m.root, "schema-generation.json"), r.Binding.Generation); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.writeJournal(j); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(m.root, j.Recovery)
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			copyPath := filepath.Join(dir, "last-copy")
			if err := os.WriteFile(copyPath, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			m.checkpoint = func(point string) error {
				if point == "resolution-selected" {
					return syscall.EIO
				}
				return nil
			}
			if err := m.resolve(context.Background(), j); !errors.Is(err, syscall.EIO) {
				t.Fatal(err)
			}
			if _, err := os.Stat(copyPath); err != nil {
				t.Fatal("selection failure discarded recovery copy", err)
			}
			if _, err := os.Stat(m.journalPath()); err != nil {
				t.Fatal("selection failure discarded journal", err)
			}
			generation, err := m.resolutionGeneration()
			if err != nil {
				t.Fatal(err)
			}
			binding, err := m.journalResolution(j, generation)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.readResolution(binding, resolutionSelected); err != nil {
				t.Fatal(err)
			}
			m.checkpoint = nil
			if err := m.resolve(context.Background(), j); err == nil {
				t.Fatal("synthetic journal bypassed installed database verification")
			}
			if _, err := os.Stat(m.journalPath()); err != nil {
				t.Fatal("unverified destination lost journal", err)
			}
			fresh := &Manager{root: m.root, cfg: m.cfg}
			if _, err := fresh.readResolution(binding, resolutionSelected); err != nil {
				t.Fatal("retained selected identity lost", err)
			}
			if _, err := fresh.readResolution(binding, resolutionReady); err == nil {
				t.Fatal("selection became release authority")
			}
		})
	}
}

func TestResolutionSelectionFaultRetainsJournalAndCopy(t *testing.T) {
	m, r := resolutionFixture(t)
	j := journal{Phase: "committed", Recovery: strings.Repeat("ab", 32) + "-recovery", Destination: m.destinationIdentity(), UploadDir: m.cfg.UploadDir}
	if err := atomicJSON(filepath.Join(m.root, "schema-generation.json"), r.Binding.Generation); err != nil {
		t.Fatal(err)
	}
	if err := m.writeJournal(j); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(m.root, j.Recovery)
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	m.checkpoint = func(point string) error {
		if point == "resolution-directory-sync" {
			return syscall.EIO
		}
		return nil
	}
	if err := m.resolve(context.Background(), j); !errors.Is(err, syscall.EIO) {
		t.Fatal(err)
	}
	for _, path := range []string{m.journalPath(), dir} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("lost recovery evidence", err)
		}
	}
	m.checkpoint = nil
	if err := m.selectedResolution(j); err != nil {
		t.Fatal("could not reconcile selection publication", err)
	}
	if err := m.resolve(context.Background(), j); err == nil {
		t.Fatal("synthetic journal authorized release without installed verification")
	}
}

func TestResolutionCommittedErrorNeverCompletes(t *testing.T) {
	m, _ := resolutionFixture(t)
	id := strings.Repeat("ab", 32)
	j := &job{Operation: Operation{ID: id, Kind: "restore", Phase: "committing", ExpiresAt: time.Now().Add(time.Hour)}, actor: 7, committed: true, active: true, dir: filepath.Join(m.root, id)}
	m.jobs[id] = j
	m.wg.Add(1)
	m.run(j, func() {}, func(context.Context) error { return syscall.EIO })
	m.wg.Wait()
	for _, status := range []func() (Operation, error){
		func() (Operation, error) { return m.Status(7, id) },
		func() (Operation, error) { return m.StatusCapability(id) },
	} {
		op, err := status()
		if err != nil || op.Phase != "recovery-required" || op.Failure.DiagnosticID == "" || op.Error == "" {
			t.Fatalf("false terminal success or lost diagnostic: %#v %v", op, err)
		}
	}
	if _, err := m.Status(8, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("wrong actor acquired status", err)
	}
	if _, err := m.StatusCapability(resolutionHash("status", id)); !errors.Is(err, ErrNotFound) {
		t.Fatal("digest acquired status capability", err)
	}
}

func TestResolutionSelectionRejectsLostGeneration(t *testing.T) {
	for _, contents := range []string{"", `"not-a-generation"`, `null`} {
		t.Run(contents, func(t *testing.T) {
			m, _ := resolutionFixture(t)
			j := journal{Phase: "committed", Recovery: strings.Repeat("ab", 32) + "-recovery", Destination: m.destinationIdentity(), UploadDir: m.cfg.UploadDir}
			if err := m.writeJournal(j); err != nil {
				t.Fatal(err)
			}
			if contents != "" {
				if err := os.WriteFile(filepath.Join(m.root, "schema-generation.json"), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.resolve(context.Background(), j); err == nil {
				t.Fatal("destructive decision invented a lost generation")
			}
			if _, err := os.Stat(m.journalPath()); err != nil {
				t.Fatal("lost identifying journal", err)
			}
		})
	}
}
