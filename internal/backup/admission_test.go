package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

func TestResolutionStartupRetainsExclusiveLeaseOnVerificationFailure(t *testing.T) {
	root := t.TempDir()
	c, err := instancecontrol.New(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{DSN: "invalid-destination", UploadDir: filepath.Join(root, "uploads"), StateDir: c.StateDir(), Control: c, BuildKind: BuildDevelopment})
	if err != nil {
		t.Fatal(err)
	}
	s, err := m.BeginStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := m.maintenance.Transition()
	if _, err = c.OperationLock(); !errors.Is(err, instancecontrol.ErrOperationBusy) {
		t.Fatal("startup lost operation reservation", err)
	}
	deny := func() {
		t.Helper()
		_, release, err := c.Enter(context.Background())
		if release != nil {
			release()
		}
		if !errors.Is(err, instancecontrol.ErrMaintenance) {
			t.Fatal("startup admitted before verification", err)
		}
	}
	deny()
	if err = s.Finish(context.Background()); err == nil {
		t.Fatal("unverified installed database opened")
	}
	deny()
	s.Close()
	deny()
	s, err = m.BeginStartup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if m.maintenance.Transition() != id {
		t.Fatal("bootstrap identity changed across failed verification")
	}
	if _, err = os.Stat(filepath.Join(m.root, "schema-generation.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed verification invented a generation", err)
	}
}

func TestResolutionReadyRecordCannotMintLeaseAuthority(t *testing.T) {
	m, r := resolutionFixture(t)
	for _, stage := range []resolutionStage{resolutionSelected, resolutionResolved, resolutionReady} {
		r.Stage = stage
		if err := m.persistResolution(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicJSON(filepath.Join(m.root, "schema-generation.json"), r.Binding.Generation); err != nil {
		t.Fatal(err)
	}
	if err := m.retainResolution(r.Binding); err != nil {
		t.Fatal(err)
	}
	if err := m.maintenance.Open(context.Background()); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("record syntax minted verification", err)
	}
	if _, err := m.PrepareAdmission(context.Background(), nil); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatal("unrelated lease minted verification", err)
	}
}

// Synthetic verification entitlement isolates the real Manager/Control reader
// composition. This is filesystem qualification, not installed-engine proof.
func TestResolutionAdmissionReadsExactSummaryAfterOwnerExit(t *testing.T) {
	m, r := resolutionFixture(t)
	for _, stage := range []resolutionStage{resolutionSelected, resolutionResolved, resolutionReady} {
		r.Stage = stage
		if err := m.persistResolution(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicJSON(filepath.Join(m.root, "schema-generation.json"), r.Binding.Generation); err != nil {
		t.Fatal(err)
	}
	m.releaseBinding = &r.Binding
	if err := m.clear(); err != nil {
		t.Fatal(err)
	}
	m.maintenance.Close()
	c, err := instancecontrol.New(m.cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := m.cfg
	cfg.Control = c
	fresh, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	deny := func() {
		t.Helper()
		_, release, err := c.Enter(context.Background())
		if release != nil {
			release()
		}
		if !errors.Is(err, instancecontrol.ErrMaintenance) {
			t.Fatal("unverified admission", err)
		}
	}
	for _, point := range []string{"resolution-read-file-sync", "resolution-read-directory-sync"} {
		fresh.checkpoint = func(p string) error {
			if p == point {
				return syscall.EIO
			}
			return nil
		}
		for range 3 {
			deny()
		}
	}
	fresh.checkpoint = nil
	_, release, err := c.Enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err = atomicJSON(filepath.Join(m.root, "schema-generation.json"), resolutionHash("wrong", "generation")); err != nil {
		t.Fatal(err)
	}
	deny()
	if err = atomicJSON(filepath.Join(m.root, "schema-generation.json"), r.Binding.Generation); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(m.resolutionPath(r.Binding)); err != nil {
		t.Fatal(err)
	}
	deny()
}
