package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// statusEvidence is the initiation owner's restart-safe status projection, not a
// dispatch receipt or recovery decision. Only hashes of existing capabilities are
// stored. The original initiation receipt remains responsible for deduplication.
type statusEvidence struct {
	Version                            int
	Operation, Destination, UploadRoot string
	Actor                              int64
	Kind, Phase                        string
	ReleaseTransition                  string
	ActivityPending                    bool
	Binding                            *resolutionBinding
	Diagnostics                        []resolutionDiagnostic
}

func (m *Manager) statusPath(id string) string {
	return m.statusDigestPath(resolutionHash("status", id))
}

func (m *Manager) statusDigestPath(operation string) string {
	return filepath.Join(m.initiationRoot(), "status-"+operation+".json")
}

func (m *Manager) validateStatus(s statusEvidence, id string) error {
	if !resolutionHex(id, 32) || s.Version != 1 || s.Operation != id || s.Actor <= 0 || s.Destination != m.destinationIdentity() || s.UploadRoot != resolutionHash("upload-root", m.cfg.UploadDir) {
		return ErrNotFound
	}
	if s.Kind != "backup" && s.Kind != "upload" && s.Kind != "restore" {
		return ErrNotFound
	}
	switch s.Phase {
	case "queued", "ready", "complete", "failed", "recovery-required":
	default:
		return ErrNotFound
	}
	if len(s.Diagnostics) > 256 {
		return ErrNotFound
	}
	seen := map[string]bool{}
	for _, d := range s.Diagnostics {
		if failureMessages[d.Category] == "" || !resolutionHex(d.ID, 16) || seen[d.ID] {
			return ErrNotFound
		}
		seen[d.ID] = true
	}
	if s.Binding != nil {
		b := *s.Binding
		if !resolutionHex(s.ReleaseTransition, 32) || b.Operation != s.Operation || b.Destination != s.Destination || b.UploadRoot != s.UploadRoot || validateResolution(resolutionRecord{Version: 1, Binding: b, Stage: resolutionSelected}) != nil {
			return ErrNotFound
		}
	}
	return nil
}

func (m *Manager) loadStatus(id string) (statusEvidence, error) {
	if !resolutionHex(id, 32) {
		return statusEvidence{}, ErrNotFound
	}
	return m.loadStatusDigest(resolutionHash("status", id))
}

// Internal activity reconciliation uses the digest, never a persisted bearer.
// Public queries must always enter through loadStatus.
func (m *Manager) loadStatusDigest(id string) (statusEvidence, error) {
	var s statusEvidence
	if !resolutionHex(id, 32) {
		return s, ErrNotFound
	}
	path := m.statusDigestPath(id)
	dirFD, err := syscall.Open(m.initiationRoot(), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return s, err
	}
	dir := os.NewFile(uintptr(dirFD), m.initiationRoot())
	defer dir.Close()
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return s, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return s, ErrNotFound
	}
	data, err := io.ReadAll(io.LimitReader(f, resolutionLimit+1))
	if err != nil || len(data) > resolutionLimit || json.Unmarshal(data, &s) != nil {
		return statusEvidence{}, ErrNotFound
	}
	canonical, err := json.Marshal(s)
	if err != nil || !bytes.Equal(bytes.TrimSpace(data), canonical) || m.validateStatus(s, id) != nil {
		return statusEvidence{}, ErrNotFound
	}
	if err = m.check("status-read-sync"); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = dir.Sync()
	}
	if err == nil {
		err = resolutionCurrent(f, path)
	}
	if err == nil {
		err = resolutionCurrent(dir, m.initiationRoot())
	}
	if err != nil {
		return statusEvidence{}, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return statusEvidence{}, err
	}
	again, err := io.ReadAll(io.LimitReader(f, resolutionLimit+1))
	if err != nil || !bytes.Equal(data, again) {
		return statusEvidence{}, ErrNotFound
	}
	return s, nil
}

// Caller owns OperationLock. Missing/corrupt prior evidence cannot be repaired by
// an outcome write. Atomic write ambiguity must propagate to the command owner.
func (m *Manager) storeStatus(id string, next statusEvidence, initial bool) error {
	if err := m.validateStatus(next, resolutionHash("status", id)); err != nil {
		return err
	}
	if err := privateDir(m.initiationRoot()); err != nil {
		return err
	}
	if err := syncDir(m.cfg.StateDir); err != nil {
		return err
	}
	old, err := m.loadStatus(id)
	if initial {
		if !errors.Is(err, os.ErrNotExist) {
			return ErrInvalid
		}
	} else {
		if err != nil {
			return err
		}
		if old.Actor != next.Actor || len(next.Diagnostics) < len(old.Diagnostics) {
			return ErrInvalid
		}
		for i, d := range old.Diagnostics {
			if next.Diagnostics[i] != d {
				return ErrInvalid
			}
		}
		if old.Binding != nil && (next.Binding == nil || *old.Binding != *next.Binding || old.ReleaseTransition != next.ReleaseTransition) {
			return ErrInvalid
		}
	}
	return m.writeStatus(id, next)
}

func (m *Manager) writeStatus(id string, s statusEvidence) error {
	return m.writeStatusDigest(resolutionHash("status", id), s)
}

func (m *Manager) writeStatusDigest(operation string, s statusEvidence) error {
	data, err := json.Marshal(s)
	if err != nil || len(data) > resolutionLimit {
		return ErrInvalid
	}
	f, err := os.CreateTemp(m.initiationRoot(), ".status-*")
	if err != nil {
		return err
	}
	defer f.Close()
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if err = m.check("status-write"); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = m.check("status-file-sync")
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = m.check("status-rename")
	}
	if err == nil {
		err = os.Rename(f.Name(), m.statusDigestPath(operation))
	}
	if err == nil {
		err = m.check("status-directory-sync")
	}
	if err == nil {
		err = syncDir(m.initiationRoot())
	}
	return err
}

func (m *Manager) startStatus(j *job) error {
	s := statusEvidence{Version: 1, Operation: resolutionHash("status", j.ID), Destination: m.destinationIdentity(), UploadRoot: resolutionHash("upload-root", m.cfg.UploadDir), Actor: j.actor, Kind: j.Kind, Phase: "queued"}
	return m.storeStatus(j.ID, s, true)
}

func (m *Manager) finishStatus(j *job) error {
	s, err := m.loadStatus(j.ID)
	if err != nil {
		return err
	}
	s.Kind, s.Phase = j.Kind, j.Phase
	s.ActivityPending = j.ActivityPending
	for _, f := range append(append([]Failure(nil), j.Diagnostics...), j.Failure) {
		if f.DiagnosticID == "" {
			continue
		}
		d := resolutionDiagnostic{Category: f.Category, ID: f.DiagnosticID}
		found := false
		for _, prior := range s.Diagnostics {
			if prior == d {
				found = true
			}
		}
		if !found {
			s.Diagnostics = append(s.Diagnostics, d)
		}
	}
	return m.storeStatus(j.ID, s, false)
}

func (m *Manager) bindStatus(id string, b resolutionBinding, diagnostics ...resolutionDiagnostic) error {
	s, err := m.loadStatus(id)
	// Legacy journals predate status evidence. Never invent an actor for them.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if m.maintenance == nil {
		return ErrRecoveryRequired
	}
	s.Binding = &b
	for _, d := range diagnostics {
		found := false
		for _, prior := range s.Diagnostics {
			if prior == d {
				found = true
			}
		}
		if !found {
			s.Diagnostics = append(s.Diagnostics, d)
		}
	}
	if s.ReleaseTransition == "" {
		s.ReleaseTransition = m.maintenance.Transition()
	}
	s.Kind, s.Phase = "restore", "recovery-required"
	return m.storeStatus(id, s, false)
}

func (m *Manager) reconstructedStatus(actor int64, id string, capability bool) (Operation, error) {
	s, err := m.loadStatus(id)
	if err != nil || (!capability && s.Actor != actor) {
		return Operation{}, ErrNotFound
	}
	op := Operation{ID: id, Kind: s.Kind, Phase: s.Phase, Reconstructed: true, ActivityPending: s.ActivityPending}
	for _, d := range s.Diagnostics {
		f := Failure{Category: d.Category, DiagnosticID: d.ID, Message: failureMessages[d.Category]}
		op.Diagnostics = append(op.Diagnostics, f)
	}
	if len(op.Diagnostics) > 0 {
		op.Failure = op.Diagnostics[0]
		op.Error = op.Failure.Message
	}
	// Interrupted process-local work is unknown, never a downloadable/retryable job.
	if op.Phase == "queued" || op.Phase == "ready" {
		op.Phase = "recovery-required"
	}
	if s.Binding != nil {
		op.Outcome = s.Binding.Outcome
		op.Phase, op.Release = "recovery-required", "unresolved"
		data, _ := json.Marshal(*s.Binding)
		if m.cfg.Control.ConfirmResolution(s.ReleaseTransition, data) == nil {
			op.Release = "verified-open"
			if s.Binding.Outcome == "restore" {
				op.Phase = "complete"
			} else {
				op.Phase = "failed"
			}
		}
	} else if op.Kind == "restore" && op.Phase == "complete" {
		op.Phase = "recovery-required"
	}
	return op, nil
}
