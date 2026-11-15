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

// selectedResolution retains the journal's decision before cleanup can remove
// its identity. Selection is deliberately not installed-state verification and
// cannot be used as a release-ready proof.
func (m *Manager) selectedResolution(j journal) error {
	generation, err := m.resolutionGeneration()
	if errors.Is(err, os.ErrNotExist) && j.Phase == "capturing" {
		// No replacement has occurred. Establish the previously absent pool
		// generation while the operation and maintenance locks are held.
		if err = m.advanceGeneration(); err == nil {
			generation, err = m.resolutionGeneration()
		}
	}
	if err != nil {
		return err
	}
	binding, err := m.journalResolution(j, generation)
	if err != nil {
		return err
	}
	r := resolutionRecord{Version: 1, Binding: binding, Stage: resolutionSelected}
	// A retry must preserve stage and diagnostic history, including evidence
	// whose final publication acknowledgment was lost.
	dir, err := m.resolutionDirectory()
	if err != nil {
		return err
	}
	defer dir.Close()
	old, err := m.readResolutionAt(dir, binding, "")
	var problem *resolutionError
	if err == nil {
		r = old
	} else if !errors.As(err, &problem) || problem.Kind != "missing" {
		return err
	}
	if j.Outcome != nil && j.Outcome.DiagnosticID != "" {
		d := resolutionDiagnostic{Category: FailureCategory(j.Outcome.FailureCategory), ID: j.Outcome.DiagnosticID}
		found := false
		for _, prior := range r.Diagnostics {
			if prior.ID == d.ID {
				if prior != d {
					return resolutionErr("mismatch", "diagnostic-history", nil)
				}
				found = true
			}
		}
		if !found {
			r.Diagnostics = append(r.Diagnostics, d)
		}
	}
	if err = m.persistResolution(r); err != nil {
		return err
	}
	return m.retainResolution(binding)
}

// Read and synchronize the exact generation publication, not a cached pool
// generation or a newly minted value after destructive replacement.
func (m *Manager) resolutionGeneration() (string, error) {
	dir, err := m.resolutionDirectory()
	if err != nil {
		return "", err
	}
	defer dir.Close()
	path := filepath.Join(m.root, "schema-generation.json")
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", resolutionErr("corrupt", "generation", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return "", err
	}
	var generation string
	if json.Unmarshal(data, &generation) != nil || !resolutionHex(generation, 32) {
		return "", resolutionErr("corrupt", "generation", nil)
	}
	if err = f.Sync(); err == nil {
		err = dir.Sync()
	}
	if err == nil {
		err = resolutionCurrent(f, path)
	}
	if err == nil {
		err = resolutionCurrent(dir, m.root)
	}
	if err != nil {
		return "", err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	again, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil || !bytes.Equal(data, again) {
		return "", resolutionErr("mismatch", "generation", err)
	}
	return generation, nil
}
