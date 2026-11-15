package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/freefsm-project/freefsm/internal/instancecontrol"
)

// VerifyAdmission is read-only persistence reconciliation under Control's D1
// locks. It never chooses a generation or performs recovery/cleanup.
func (m *Manager) VerifyAdmission(data json.RawMessage) error {
	var b resolutionBinding
	if json.Unmarshal(data, &b) != nil {
		return ErrRecoveryRequired
	}
	canonical, err := json.Marshal(b)
	if err != nil || string(canonical) != string(data) {
		return ErrRecoveryRequired
	}
	if b.Destination != m.destinationIdentity() || b.UploadRoot != resolutionHash("upload-root", m.cfg.UploadDir) {
		return ErrRecoveryRequired
	}
	if _, err = m.readResolution(b, resolutionReady); err != nil {
		return err
	}
	generation, err := m.resolutionGeneration()
	if err != nil {
		return err
	}
	if b.Generation != generation {
		return ErrRecoveryRequired
	}
	return nil
}

// PrepareAdmission accepts only the lease held by this Manager's verification
// path. Neither raw binding strings nor a ready file can create this entitlement.
func (m *Manager) PrepareAdmission(ctx context.Context, lease *instancecontrol.MaintenanceLease) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if lease != m.maintenance || m.releaseBinding == nil {
		return nil, ErrRecoveryRequired
	}
	b, err := json.Marshal(*m.releaseBinding)
	if err != nil {
		return nil, err
	}
	if err = m.VerifyAdmission(b); err != nil {
		return nil, err
	}
	return b, nil
}

func (m *Manager) beginMaintenance(ctx context.Context) (func(), error) {
	if m.maintenance != nil {
		return nil, ErrRecoveryRequired
	}
	l, err := m.cfg.Control.BeginMaintenance(ctx)
	if err != nil {
		return nil, err
	}
	m.maintenance = l
	m.releaseBinding = nil
	return func() { l.Close(); m.maintenance = nil; m.releaseBinding = nil }, nil
}

func (m *Manager) retainResolution(b resolutionBinding) error {
	if m.maintenance == nil {
		return ErrRecoveryRequired
	}
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return m.maintenance.RetainResolution(data)
}

func (m *Manager) advanceResolution(b resolutionBinding, stage resolutionStage) error {
	dir, err := m.resolutionDirectory()
	if err != nil {
		return err
	}
	defer dir.Close()
	r, err := m.readResolutionAt(dir, b, "")
	if err != nil {
		return err
	}
	if stageRank(r.Stage) >= stageRank(stage) {
		return nil
	}
	r.Stage = stage
	return m.persistResolution(r)
}

// verifyInstalled runs against the actual destination while exclusive admission
// is held. Journal selection still owns recovery; this is not a second engine.
func (m *Manager) verifyInstalled(ctx context.Context) error {
	if _, err := m.mappings(ctx); err != nil {
		return err
	}
	credentials, err := m.providerCredentials(ctx)
	if err != nil {
		return err
	}
	if err = verifyProviderKeys(credentials); err != nil {
		return err
	}
	if _, err = inventory(m.cfg.UploadDir); err != nil {
		return err
	}
	return syncDir(m.cfg.UploadDir)
}

func (m *Manager) retainedBinding() (resolutionBinding, error) {
	var b resolutionBinding
	if m.maintenance == nil {
		return b, ErrRecoveryRequired
	}
	data := m.maintenance.PreviousResolution()
	if len(data) == 0 || string(data) == "null" {
		return b, os.ErrNotExist
	}
	if json.Unmarshal(data, &b) != nil {
		return b, ErrRecoveryRequired
	}
	if err := validateResolution(resolutionRecord{Version: 1, Binding: b, Stage: resolutionSelected}); err != nil {
		return b, err
	}
	if b.Destination != m.destinationIdentity() || b.UploadRoot != resolutionHash("upload-root", m.cfg.UploadDir) {
		return b, ErrRecoveryRequired
	}
	return b, nil
}

func (m *Manager) finishAdmission(ctx context.Context) error {
	b, bindingErr := m.retainedBinding()
	if bindingErr != nil && !errors.Is(bindingErr, os.ErrNotExist) {
		return bindingErr
	}
	if err := m.verifyInstalled(ctx); err != nil {
		return err
	}
	generation, err := m.resolutionGeneration()
	if errors.Is(err, os.ErrNotExist) && errors.Is(bindingErr, os.ErrNotExist) {
		if err = m.advanceGeneration(); err == nil {
			generation, err = m.resolutionGeneration()
		}
	}
	if err != nil {
		return err
	}
	if errors.Is(bindingErr, os.ErrNotExist) {
		b = resolutionBinding{Kind: "bootstrap", Transition: m.maintenance.Transition(), Destination: m.destinationIdentity(), UploadRoot: resolutionHash("upload-root", m.cfg.UploadDir), Generation: generation, Outcome: "no-replacement"}
		if err = m.persistResolution(resolutionRecord{Version: 1, Binding: b, Stage: resolutionSelected}); err != nil {
			return err
		}
		if err = m.retainResolution(b); err != nil {
			return err
		}
	}
	if b.Generation != generation {
		return ErrRecoveryRequired
	}
	if err = m.replayPending(ctx); err != nil {
		return err
	}
	if err = m.advanceResolution(b, resolutionResolved); err != nil {
		return err
	}
	if err = m.requireNoJournal(); err != nil {
		return err
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err = cleanupAbandoned(filepath.Join(m.root, entry.Name())); err != nil {
				return err
			}
		}
	}
	if err = syncDir(m.root); err != nil {
		return err
	}
	if m.cfg.ResetConnections != nil {
		m.cfg.ResetConnections()
	}
	if err = m.advanceResolution(b, resolutionReady); err != nil {
		return err
	}
	if err = m.check("release-ready"); err != nil {
		return err
	}
	m.releaseBinding = &b
	if err = m.clear(); err != nil {
		return err
	}
	return m.check("admission-opened")
}

// Startup retains operation and exclusive maintenance through recovery and the
// caller's migration/initialization. Failure or Close alone never opens admission.
type Startup struct {
	m               *Manager
	release, unlock func()
	done            bool
}

func (m *Manager) BeginStartup(ctx context.Context) (*Startup, error) {
	unlock, err := m.cfg.Control.OperationLock()
	if err != nil {
		return nil, err
	}
	release, err := m.beginMaintenance(ctx)
	if err != nil {
		unlock()
		return nil, err
	}
	s := &Startup{m: m, release: release, unlock: unlock}
	if err = m.recoverClosed(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Startup) Close() {
	if !s.done {
		s.done = true
		s.release()
		s.unlock()
	}
}
func (s *Startup) Finish(ctx context.Context) error {
	if s.done {
		return ErrRecoveryRequired
	}
	if err := s.m.finishAdmission(ctx); err != nil {
		return err
	}
	s.Close()
	return nil
}

func (m *Manager) recoverClosed(ctx context.Context) error {
	b, err := os.ReadFile(m.journalPath())
	if err == nil {
		var j journal
		if err = json.Unmarshal(b, &j); err != nil {
			return err
		}
		return m.resolve(ctx, j)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// A missing journal only means there is no journal to execute. Finish still
	// reconciles retained identity, installed state, activity and cleanup.
	return nil
}
