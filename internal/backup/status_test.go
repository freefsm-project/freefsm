package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func statusRestart(t *testing.T, m *Manager) *Manager {
	t.Helper()
	c, err := instancecontrol.New(m.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	cfg.Control = c
	next, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func statusJob() *job {
	return &job{Operation: Operation{ID: strings.Repeat("ab", 32), Kind: "restore", Phase: "queued"}, actor: 17}
}

func TestDurableStatusAuthorizationAndPrivacy(t *testing.T) {
	m, _ := resolutionFixture(t)
	j := statusJob()
	if err := m.startStatus(j); err != nil {
		t.Fatal(err)
	}
	n := statusRestart(t, m)
	for _, query := range []func() (Operation, error){
		func() (Operation, error) { return n.Status(18, j.ID) },
		func() (Operation, error) { return n.StatusCapability(resolutionHash("status", j.ID)) },
		func() (Operation, error) { return n.StatusCapability(strings.Repeat("ac", 32)) },
		func() (Operation, error) { return n.StatusCapability("../private") },
	} {
		op, err := query()
		if !errors.Is(err, ErrNotFound) || op.ID != "" {
			t.Fatalf("unauthorized status: %+v %v", op, err)
		}
	}
	for _, query := range []func() (Operation, error){func() (Operation, error) { return n.Status(17, j.ID) }, func() (Operation, error) { return n.StatusCapability(j.ID) }} {
		op, err := query()
		if err != nil || !op.Reconstructed || op.Phase != "recovery-required" || op.SourceName != "" {
			t.Fatalf("status: %+v %v", op, err)
		}
	}
	b, err := os.ReadFile(m.statusPath(j.ID))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{j.ID, m.cfg.DSN, m.cfg.UploadDir} {
		if strings.Contains(string(b), secret) {
			t.Fatal("private value persisted")
		}
	}
	if err := os.WriteFile(m.statusPath(j.ID), []byte(`{"Version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.StatusCapability(j.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := m.finishStatus(j); err == nil {
		t.Fatal("overwrote corrupt evidence")
	}
}

// Synthetic verification entitlement exercises actual files, locks, Manager and
// Control. This is explicitly not a whole-engine installed-generation proof.
func TestDurableStatusRequiresCurrentVerifiedOpen(t *testing.T) {
	for _, outcome := range []string{"restore", "rollback", "no-replacement"} {
		t.Run(outcome, func(t *testing.T) {
			m, r := resolutionFixture(t)
			j := statusJob()
			r.Binding.Outcome = outcome
			r.Binding.JournalPhase = map[string]string{"restore": "committed", "rollback": "rolled-back", "no-replacement": "capturing"}[outcome]
			if err := m.startStatus(j); err != nil {
				t.Fatal(err)
			}
			j.Phase = "recovery-required"
			j.Failure = Failure{Category: FailureStorage, DiagnosticID: strings.Repeat("de", 16)}
			if err := m.finishStatus(j); err != nil {
				t.Fatal(err)
			}
			if err := m.bindStatus(j.ID, r.Binding); err != nil {
				t.Fatal(err)
			}
			n := statusRestart(t, m)
			check := func(want string) Operation {
				t.Helper()
				op, err := n.StatusCapability(j.ID)
				if err != nil || op.Phase != want {
					t.Fatalf("want %s: %+v %v", want, op, err)
				}
				return op
			}
			for _, stage := range []resolutionStage{resolutionSelected, resolutionResolved, resolutionReady} {
				r.Stage = stage
				if err := m.persistResolution(r); err != nil {
					t.Fatal(err)
				}
				check("recovery-required")
			}
			if err := atomicJSON(filepath.Join(m.root, "schema-generation.json"), r.Binding.Generation); err != nil {
				t.Fatal(err)
			}
			m.releaseBinding = &r.Binding
			if err := m.clear(); err != nil {
				t.Fatal(err)
			}
			m.maintenance.Close()
			want := "failed"
			if outcome == "restore" {
				want = "complete"
			}
			if op := check(want); op.Release != "verified-open" || op.Outcome != outcome || op.Failure.DiagnosticID != j.Failure.DiagnosticID {
				t.Fatal(op)
			}
			n.checkpoint = func(point string) error {
				if point == "resolution-read-file-sync" {
					return syscall.EIO
				}
				return nil
			}
			check("recovery-required")
			n.checkpoint = nil
			check(want)
			if err := atomicJSON(filepath.Join(m.root, "schema-generation.json"), strings.Repeat("ef", 32)); err != nil {
				t.Fatal(err)
			}
			check("recovery-required")
			if err := atomicJSON(filepath.Join(m.root, "schema-generation.json"), r.Binding.Generation); err != nil {
				t.Fatal(err)
			}
			lease, err := m.cfg.Control.BeginMaintenance(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			m.maintenance = lease
			check("recovery-required")
			if err = m.clear(); err != nil {
				t.Fatal(err)
			}
			lease.Close()
			// Even reopening the same generation in a NEW transition is not the
			// old operation's release acknowledgement.
			check("recovery-required")
		})
	}
}

func TestDurableStatusFaultsAndDiagnosticRetention(t *testing.T) {
	for _, point := range []string{"status-write", "status-file-sync", "status-rename", "status-directory-sync"} {
		t.Run(point, func(t *testing.T) {
			m, _ := resolutionFixture(t)
			j := statusJob()
			if err := m.startStatus(j); err != nil {
				t.Fatal(err)
			}
			j.Phase = "failed"
			j.Failure = Failure{Category: FailureArchiveIntegrity, DiagnosticID: strings.Repeat("ab", 16)}
			m.checkpoint = func(p string) error {
				if p == point {
					return syscall.EIO
				}
				return nil
			}
			if err := m.finishStatus(j); !errors.Is(err, syscall.EIO) {
				t.Fatal(err)
			}
			m.checkpoint = nil
			if err := m.finishStatus(j); err != nil {
				t.Fatal(err)
			}
			j.Failure = Failure{Category: FailureStorage, DiagnosticID: strings.Repeat("cd", 16)}
			if err := m.finishStatus(j); err != nil {
				t.Fatal(err)
			}
			n := statusRestart(t, m)
			op, err := n.Status(17, j.ID)
			if err != nil || op.Phase != "failed" || len(op.Diagnostics) != 2 || op.Failure.Category != FailureArchiveIntegrity {
				t.Fatalf("lost original: %+v %v", op, err)
			}
			s, err := m.loadStatus(j.ID)
			if err != nil {
				t.Fatal(err)
			}
			s.Diagnostics = s.Diagnostics[1:]
			if err = m.storeStatus(j.ID, s, false); err == nil {
				t.Fatal("discarded diagnostic")
			}
			n.checkpoint = func(string) error { return syscall.EIO }
			if _, err = n.StatusCapability(j.ID); !errors.Is(err, ErrNotFound) {
				t.Fatal("unsynced status disclosed", err)
			}
		})
	}
}

func TestDurableStatusKnownFailureWithPendingActivity(t *testing.T) {
	root := t.TempDir()
	c, err := instancecontrol.New(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{DSN: "no-db", UploadDir: filepath.Join(root, "uploads"), StateDir: c.StateDir(), Control: c, BuildKind: BuildDevelopment, ActivitySink: func(context.Context, ActivityEvent) error { return syscall.EIO }})
	if err != nil {
		t.Fatal(err)
	}
	j, unlock, err := m.newJob(17, "backup")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.beginActivity(j, ActivityActor{ID: 17, Name: "private actor"}, "backup_failed"); err != nil {
		t.Fatal(err)
	}
	m.run(j, unlock, func(context.Context) error {
		return classified(FailureArchiveIntegrity, errors.New("private path DSN"))
	})
	m.wg.Wait()
	n := statusRestart(t, m)
	op, err := n.Status(17, j.ID)
	if err != nil || op.Phase != "failed" || !op.ActivityPending || len(op.Diagnostics) != 2 || op.Failure.Category != FailureArchiveIntegrity {
		t.Fatalf("known failure lost: %+v %v", op, err)
	}
	b, _ := json.Marshal(op)
	if strings.Contains(string(b), "private") {
		t.Fatal("private diagnostic leaked")
	}
}
