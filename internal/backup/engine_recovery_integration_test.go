package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func TestEngineRecoveryFaultOutcomes(t *testing.T) {
	f := newCrashFixture(t)
	src := f.manager(t, "source")
	seedCrashSessions(t, src, "source")
	archive := createCrashArchive(t, src)
	for _, scenario := range []struct {
		point, outcome, generation string
	}{
		{"archive-changed", "no-replacement", "destination"},
		{"protections-applied", "rollback", "destination"},
		{"recovery-directory-sync", "restore", "source"},
		{"journal-removed", "restore", "source"},
		{"release-ready", "restore", "source"},
	} {
		for _, disabled := range []bool{false, true} {
			name := scenario.point + "/enabled"
			if disabled {
				name = scenario.point + "/disabled"
			}
			t.Run(name, func(t *testing.T) {
				dst := f.manager(t, "destination")
				seedCrashSessions(t, dst, "destination")
				if disabled {
					disableCrashEmail(t, dst)
				}
				op := stageCrashArchive(t, dst, archive)
				if scenario.point == "archive-changed" {
					if err := os.WriteFile(filepath.Join(dst.root, op.ID, "archive.age"), []byte("changed after validated upload"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				hit := false
				dst.checkpoint = func(point string) error {
					if point == scenario.point {
						hit = true
						return syscall.EIO
					}
					return nil
				}
				if _, err := dst.StartRestore(context.Background(), 1, op.ID, "destination", "destination"); err != nil {
					t.Fatal(err)
				}
				op = awaitOperation(t, dst, 1, op.ID)
				if scenario.point != "archive-changed" && !hit {
					t.Fatal("fault boundary not reached")
				}
				dst.checkpoint = nil
				if op.Phase == "complete" || op.Failure.DiagnosticID == "" {
					t.Fatalf("fault falsely completed/lost diagnostic: %+v", op)
				}
				assertCrashState(t, dst, scenario.generation)
				if scenario.outcome == "restore" {
					assertCommittedCrashState(t, dst)
					assertCrashMaintenance(t, dst)
				} else if crashEmailDisabled(t, dst) != disabled {
					t.Fatal("prior email policy not restored")
				}
				if scenario.outcome != "restore" {
					conn, err := dst.connect(context.Background())
					if err != nil {
						t.Fatal(err)
					}
					var active, destination int
					err = conn.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE expires_at>NOW() AND revoked_at IS NULL),count(*) FILTER (WHERE token_hash IN ('destination-web','destination-mobile')) FROM public.sessions`).Scan(&active, &destination)
					_ = conn.Close(context.Background())
					if err != nil || active != 2 || destination != 2 {
						t.Fatalf("prior generation sessions changed: active=%d destination=%d err=%v", active, destination, err)
					}
				}
				before, err := dst.loadStatus(op.ID)
				if err != nil || before.Binding == nil || before.Binding.Outcome != scenario.outcome {
					t.Fatalf("wrong selected outcome: %+v %v", before, err)
				}
				for range 2 {
					runCrashChild(t, dst, "", "recover", "")
					assertCrashState(t, dst, scenario.generation)
					after, err := dst.loadStatus(op.ID)
					if err != nil || !reflect.DeepEqual(before.Diagnostics, after.Diagnostics) || after.Binding == nil || *before.Binding != *after.Binding {
						t.Fatalf("recovery lost diagnostic/identity: %+v %v", after, err)
					}
				}
			})
		}
	}
}

// These barriers follow actual encrypted restore and durable generation selection.
// No test-created journal or private release entitlement stands in for recovery.
func TestEngineRecoveryLateBoundaries(t *testing.T) {
	f := newCrashFixture(t)
	src := f.manager(t, "source")
	seedCrashSessions(t, src, "source")
	archive := createCrashArchive(t, src)
	for _, point := range []string{"selected-status-directory-sync", "resolution-selected", "resolution-resolved", "journal-removed", "release-ready", "admission-opened"} {
		t.Run(point, func(t *testing.T) {
			dst := f.manager(t, "destination")
			seedCrashSessions(t, dst, "destination")
			id := runCrashChild(t, dst, archive, "restore", point)
			assertCommittedCrashState(t, dst)
			before, err := dst.loadStatus(id)
			if err != nil || before.Binding == nil || before.Binding.Outcome != "restore" {
				t.Fatalf("original selected status missing: %+v %v", before, err)
			}
			op, err := dst.StatusCapability(id)
			if err != nil || !op.Reconstructed || op.Outcome != "restore" {
				t.Fatalf("original capability lost: %+v %v", op, err)
			}
			if point == "admission-opened" {
				if op.Phase != "complete" || op.Release != "verified-open" {
					t.Fatalf("durably released operation not reconciled: %+v", op)
				}
			} else {
				assertCrashMaintenance(t, dst)
				if op.Phase == "complete" {
					t.Fatalf("selected generation falsely complete: %+v", op)
				}
			}
			for attempt := 0; attempt < 2; attempt++ {
				runCrashChild(t, dst, "", "recover", "")
				assertCommittedCrashState(t, dst)
				if _, err = os.Stat(dst.journalPath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("journal not cleaned: %v", err)
				}
				after, err := dst.loadStatus(id)
				if err != nil || after.Binding == nil || *before.Binding != *after.Binding || before.ReleaseTransition != after.ReleaseTransition {
					t.Fatalf("recovery changed operation identity: before=%+v after=%+v err=%v", before, after, err)
				}
				op, err = dst.StatusCapability(id)
				if err != nil || op.Outcome != "restore" {
					t.Fatalf("recovered capability: %+v %v", op, err)
				}
				// The first recovery of CLOSED retains the original transition.
				// A subsequent startup closes OPEN under a new transition and must
				// not acknowledge that historical operation's old release.
				wantComplete := attempt == 0 && point != "admission-opened"
				if (op.Phase == "complete") != wantComplete {
					t.Fatalf("wrong current-transition acknowledgment: %+v wantComplete=%v", op, wantComplete)
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

func TestEngineRecoveryLegacyJournalAndInstalledMarker(t *testing.T) {
	f := newCrashFixture(t)
	src := f.manager(t, "source", true)
	archive := createCrashArchive(t, src)
	for _, disabled := range []bool{false, true} {
		name := "enabled"
		if disabled {
			name = "disabled"
		}
		t.Run(name, func(t *testing.T) {
			dst := f.manager(t, "destination", true)
			if disabled {
				if err := dst.cfg.Control.DisableEmail(); err != nil {
					t.Fatal(err)
				}
			}
			id := runCrashChild(t, dst, archive, "restore", "protections-applied")
			recoveryEvidence(t, dst)
			// Legacy journals use the same recovery decision fields but predate
			// actor-bound durable status and the versioned admission discriminator.
			if err := os.Remove(dst.statusPath(id)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst.cfg.StateDir, "maintenance"), []byte("closed\n"), 0600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				runCrashChild(t, dst, "", "recover", "")
				assertCrashState(t, dst, "destination")
				if crashEmailDisabled(t, dst) != disabled {
					t.Fatal("legacy recovery changed prior enabled/disabled policy")
				}
				if _, err := dst.StatusCapability(id); !errors.Is(err, ErrNotFound) {
					t.Fatalf("legacy recovery invented status authority: %v", err)
				}
			}
		})
	}
}

func TestEngineRecoveryLegacyInstalledBootstrap(t *testing.T) {
	f := newCrashFixture(t)
	for _, marker := range []string{"missing", "closed"} {
		for _, disabled := range []bool{false, true} {
			name := marker + "/enabled"
			if disabled {
				name = marker + "/disabled"
			}
			t.Run(name, func(t *testing.T) {
				m := f.manager(t, "installed", true)
				if disabled {
					if err := m.cfg.Control.DisableEmail(); err != nil {
						t.Fatal(err)
					}
				}
				path := filepath.Join(m.cfg.StateDir, "maintenance")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if marker == "closed" {
					if err := os.WriteFile(path, []byte("closed\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Remove(filepath.Join(m.root, "schema-generation.json")); err != nil {
					t.Fatal(err)
				}
				assertCrashMaintenance(t, m)
				for range 2 {
					runCrashChild(t, m, "", "recover", "")
					assertCrashState(t, m, "installed")
					if crashEmailDisabled(t, m) != disabled {
						t.Fatal("installed legacy email state changed")
					}
				}
			})
		}
	}
}

func TestEngineRecoveryRejectsDamagedBindings(t *testing.T) {
	f := newCrashFixture(t)
	src := f.manager(t, "source")
	seedCrashSessions(t, src, "source")
	archive := createCrashArchive(t, src)
	dst := f.manager(t, "destination")
	id := runCrashChild(t, dst, archive, "restore", "admission-opened")
	s, err := dst.loadStatus(id)
	if err != nil || s.Binding == nil {
		t.Fatalf("status: %+v %v", s, err)
	}
	for _, path := range []string{filepath.Join(dst.cfg.StateDir, "maintenance"), dst.resolutionPath(*s.Binding), filepath.Join(dst.root, "schema-generation.json")} {
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, contents := range []string{"", `{"Version":99}`, `{"Version":1,"Decision":"verified-open"}`} {
			if contents == "" {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte(contents), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertCrashMaintenance(t, dst)
			op, err := dst.StatusCapability(id)
			if err != nil || op.Phase == "complete" {
				t.Fatalf("damaged binding acknowledged: %+v %v", op, err)
			}
			if err = os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertCommittedCrashState(t, dst)
	op, err := dst.StatusCapability(id)
	if err != nil || op.Phase != "complete" {
		t.Fatalf("exact binding repair not reconciled: %+v %v", op, err)
	}
}

func TestEngineRecoveryMissingJournalCannotAdmitReplacedGeneration(t *testing.T) {
	f := newCrashFixture(t)
	src := f.manager(t, "source")
	archive := createCrashArchive(t, src)
	dst := f.manager(t, "destination")
	runCrashChild(t, dst, archive, "restore", "files-entry-removed")
	original := recoveryEvidence(t, dst)
	if err := os.Remove(dst.journalPath()); err != nil {
		t.Fatal(err)
	}
	if err := dst.Recover(context.Background()); err == nil {
		t.Fatal("missing journal bootstrapped a partially replaced generation")
	}
	_, release, err := dst.cfg.Control.Enter(context.Background())
	if release != nil {
		release()
	}
	if !errors.Is(err, instancecontrol.ErrMaintenance) {
		t.Fatalf("unknown generation admitted: %v", err)
	}
	// Restore the exact original evidence, then use fresh processes to recover.
	if err := os.WriteFile(dst.journalPath(), original, 0600); err != nil {
		t.Fatal(err)
	}
	runCrashChild(t, dst, "", "recover", "")
	assertCrashState(t, dst, "destination")
}
