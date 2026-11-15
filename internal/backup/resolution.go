package backup

// This is the backup owner's durable evidence component, not an admission
// authority. See resolution_contract.md for integrated and pending call sites.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
)

type resolutionStage string

const (
	resolutionSelected resolutionStage = "selected"
	resolutionResolved resolutionStage = "resolved"
	resolutionReady    resolutionStage = "release-ready"
	resolutionLimit                    = 64 << 10
)

// All identity fields are immutable. Operation is a one-way status lookup key,
// NOT the secret Operation.ID. Transition identifies the original journal, not
// a new recovery attempt. Bootstrap has no operation or journal decision.
type resolutionBinding struct {
	Kind, Operation, Transition, Destination, UploadRoot, Generation string
	JournalPhase, Outcome                                            string
}

// Diagnostic references are append-only. Messages are projected from the fixed
// failure vocabulary, never persisted from arbitrary errors or caller strings.
type resolutionDiagnostic struct {
	Category FailureCategory
	ID       string
}

type resolutionRecord struct {
	Version     int
	Binding     resolutionBinding
	Stage       resolutionStage
	Diagnostics []resolutionDiagnostic
}

// Unresolved means publication MAY have happened; callers retain closure/evidence
// and reconcile, never compensate or report success. Error text contains no IDs.
type resolutionError struct {
	Kind, Field string
	cause       error
}

func (e *resolutionError) Error() string { return "backup resolution " + e.Kind + ": " + e.Field }
func (e *resolutionError) Unwrap() error { return e.cause }
func resolutionErr(kind, field string, cause error) error {
	return &resolutionError{Kind: kind, Field: field, cause: cause}
}

func resolutionHash(domain, value string) string {
	h := sha256.Sum256([]byte(domain + "\x00" + value))
	return hex.EncodeToString(h[:])
}

func resolutionHex(s string, n int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == n && hex.EncodeToString(b) == s
}

// journalResolution binds the existing journal's recovery identity, including
// legacy journals, without storing its bearer-bearing Recovery pathname. Calling
// this does not verify a generation: resolve must establish it under maintenance.
func (m *Manager) journalResolution(j journal, generation string) (resolutionBinding, error) {
	id := strings.TrimSuffix(j.Recovery, "-recovery")
	if id == j.Recovery || !resolutionHex(id, 32) || j.Destination != m.destinationIdentity() || j.UploadDir != m.cfg.UploadDir {
		return resolutionBinding{}, resolutionErr("mismatch", "journal", nil)
	}
	outcomes := map[string]string{"capturing": "no-replacement", "committed": "restore", "rolled-back": "rollback"}
	b := resolutionBinding{
		Kind: "journal", Operation: resolutionHash("status", id),
		Transition: resolutionHash("journal", j.Recovery), Destination: j.Destination,
		UploadRoot: resolutionHash("upload-root", j.UploadDir), Generation: generation,
		JournalPhase: j.Phase, Outcome: outcomes[j.Phase],
	}
	return b, validateResolution(resolutionRecord{Version: 1, Binding: b, Stage: resolutionSelected})
}

func validateResolution(r resolutionRecord) error {
	if r.Version != 1 {
		return resolutionErr("unsupported", "version", nil)
	}
	b := r.Binding
	for _, s := range []string{b.Transition, b.Destination, b.UploadRoot, b.Generation} {
		if !resolutionHex(s, 32) {
			return resolutionErr("corrupt", "identity", nil)
		}
	}
	switch b.Kind {
	case "journal":
		if !resolutionHex(b.Operation, 32) || !((b.JournalPhase == "capturing" && b.Outcome == "no-replacement") ||
			(b.JournalPhase == "committed" && b.Outcome == "restore") || (b.JournalPhase == "rolled-back" && b.Outcome == "rollback")) {
			return resolutionErr("corrupt", "journal", nil)
		}
	case "bootstrap":
		if b.Operation != "" || b.JournalPhase != "" || b.Outcome != "no-replacement" {
			return resolutionErr("corrupt", "bootstrap", nil)
		}
	default:
		return resolutionErr("corrupt", "kind", nil)
	}
	if stageRank(r.Stage) == 0 {
		return resolutionErr("stage", "stage", nil)
	}
	if len(r.Diagnostics) > 256 {
		return resolutionErr("corrupt", "diagnostics", nil)
	}
	seen := map[string]bool{}
	for _, d := range r.Diagnostics {
		if failureMessages[d.Category] == "" || !resolutionHex(d.ID, 16) || seen[d.ID] {
			return resolutionErr("corrupt", "diagnostics", nil)
		}
		seen[d.ID] = true
	}
	return nil
}

func stageRank(s resolutionStage) int {
	switch s {
	case resolutionSelected:
		return 1
	case resolutionResolved:
		return 2
	case resolutionReady:
		return 3
	}
	return 0
}

// Version 1 uses exactly the encoding/json canonical struct representation.
// Re-encoding rejects duplicate/case-aliased fields, null substitutions, unknown
// fields and trailing documents, rather than accepting ambiguous JSON evidence.
func decodeResolution(data []byte) (resolutionRecord, error) {
	var r resolutionRecord
	if len(data) > resolutionLimit || json.Unmarshal(data, &r) != nil {
		return r, resolutionErr("corrupt", "encoding", nil)
	}
	if err := validateResolution(r); err != nil {
		return resolutionRecord{}, err
	}
	canonical, err := json.Marshal(r)
	if err != nil || !bytes.Equal(bytes.TrimSpace(data), canonical) {
		return resolutionRecord{}, resolutionErr("corrupt", "encoding", err)
	}
	return r, nil
}

func matchResolution(got, want resolutionBinding) error {
	fields := []struct{ name, got, want string }{
		{"kind", got.Kind, want.Kind}, {"operation", got.Operation, want.Operation},
		{"transition", got.Transition, want.Transition}, {"destination", got.Destination, want.Destination},
		{"upload-root", got.UploadRoot, want.UploadRoot}, {"generation", got.Generation, want.Generation},
		{"journal-phase", got.JournalPhase, want.JournalPhase}, {"outcome", got.Outcome, want.Outcome},
	}
	for _, f := range fields {
		if f.got != f.want {
			return resolutionErr("mismatch", f.name, nil)
		}
	}
	return nil
}

func (m *Manager) resolutionPath(b resolutionBinding) string {
	return filepath.Join(m.root, "resolution-"+b.Transition+".json")
}

// Caller holds operation -> exclusive intent -> exclusive admission for writes;
// readers hold shared intent -> shared admission through the entire verification.
// No caller may mutate these files outside that protocol. The directory itself
// is persistent, never renamed by cooperating owners.
func (m *Manager) resolutionDirectory() (*os.File, error) {
	fd, err := syscall.Open(m.root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, resolutionErr("persistence", "directory-open", err)
	}
	return os.NewFile(uintptr(fd), m.root), nil
}

func resolutionCurrent(f *os.File, path string) error {
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(opened, current) || current.Mode()&os.ModeSymlink != 0 {
		return resolutionErr("mismatch", "current-path", nil)
	}
	return nil
}

func (m *Manager) readResolution(want resolutionBinding, stage resolutionStage) (resolutionRecord, error) {
	if err := validateResolution(resolutionRecord{Version: 1, Binding: want, Stage: stage}); err != nil {
		return resolutionRecord{}, err
	}
	dir, err := m.resolutionDirectory()
	if err != nil {
		return resolutionRecord{}, err
	}
	defer dir.Close()
	return m.readResolutionAt(dir, want, stage)
}

func (m *Manager) readResolutionAt(dir *os.File, want resolutionBinding, stage resolutionStage) (resolutionRecord, error) {
	var zero resolutionRecord
	path := m.resolutionPath(want)
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return zero, resolutionErr("missing", "record", err)
	}
	if err != nil {
		return zero, resolutionErr("persistence", "record-open", err)
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return zero, resolutionErr("corrupt", "file", err)
	}
	data, err := io.ReadAll(io.LimitReader(f, resolutionLimit+1))
	if err != nil {
		return zero, resolutionErr("persistence", "read", err)
	}
	r, err := decodeResolution(data)
	if err != nil {
		return zero, err
	}
	if err = matchResolution(r.Binding, want); err != nil {
		return zero, err
	}
	if stage != "" && r.Stage != stage {
		return zero, resolutionErr("stage", "stage", nil)
	}
	if err = m.check("resolution-read-file-sync"); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = m.check("resolution-read-directory-sync")
	}
	if err == nil {
		err = dir.Sync()
	}
	if err != nil {
		return zero, resolutionErr("persistence", "read-sync", err)
	}
	if err = m.check("resolution-read-current"); err == nil {
		err = resolutionCurrent(f, path)
	}
	if err == nil {
		err = resolutionCurrent(dir, m.root)
	}
	if err != nil {
		return zero, resolutionErr("mismatch", "current-path", err)
	}
	// Re-read the same descriptor after synchronization, not a newly opened file.
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return zero, resolutionErr("persistence", "read", err)
	}
	again, err := io.ReadAll(io.LimitReader(f, resolutionLimit+1))
	if err != nil || !bytes.Equal(data, again) {
		return zero, resolutionErr("mismatch", "content", err)
	}
	return r, nil
}

// persistResolution never selects a database/files generation. The owning engine
// must satisfy each stage's obligations before calling. Repeating identical data
// re-synchronizes it; changed identity, skipped/regressed stage, or lost diagnostic
// history is rejected. Existing malformed/unreadable evidence is never overwritten.
func (m *Manager) persistResolution(next resolutionRecord) error {
	if err := validateResolution(next); err != nil {
		return err
	}
	dir, err := m.resolutionDirectory() // before ANY mutation
	if err != nil {
		return err
	}
	defer dir.Close()
	old, err := m.readResolutionAt(dir, next.Binding, "")
	var problem *resolutionError
	if errors.As(err, &problem) && problem.Kind == "missing" {
		if next.Stage != resolutionSelected {
			return resolutionErr("stage", "initial", nil)
		}
	} else if err != nil {
		return err
	} else {
		delta := stageRank(next.Stage) - stageRank(old.Stage)
		if delta < 0 || delta > 1 {
			return resolutionErr("stage", "advancement", nil)
		}
		if len(next.Diagnostics) < len(old.Diagnostics) {
			return resolutionErr("mismatch", "diagnostic-history", nil)
		}
		for i, d := range old.Diagnostics {
			if next.Diagnostics[i] != d {
				return resolutionErr("mismatch", "diagnostic-history", nil)
			}
		}
		if reflect.DeepEqual(old, next) {
			return nil
		}
	}
	data, err := json.Marshal(next)
	if err != nil || len(data) > resolutionLimit {
		return resolutionErr("corrupt", "encoding", err)
	}
	if err = m.check("resolution-create"); err != nil {
		return resolutionErr("persistence", "create", err)
	}
	f, err := os.CreateTemp(m.root, ".resolution-*")
	if err != nil {
		return resolutionErr("persistence", "create", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	// CreateTemp's requested mode is filtered by the process umask. Establish the
	// exact private mode required by readers before publishing any evidence.
	if err = f.Chmod(0600); err != nil {
		return resolutionErr("persistence", "permissions", err)
	}
	if err = m.check("resolution-write"); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = m.check("resolution-file-sync")
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = resolutionCurrent(dir, m.root)
	}
	if err == nil {
		err = m.check("resolution-rename")
	}
	if err != nil {
		return resolutionErr("persistence", "prepublication", err)
	}
	if err = os.Rename(f.Name(), m.resolutionPath(next.Binding)); err != nil {
		return resolutionErr("unresolved", "rename", err)
	}
	if err = m.check("resolution-directory-sync"); err == nil {
		err = dir.Sync()
	}
	if err == nil {
		err = resolutionCurrent(f, m.resolutionPath(next.Binding))
	}
	if err == nil {
		err = resolutionCurrent(dir, m.root)
	}
	if err != nil {
		return resolutionErr("unresolved", "publication", err)
	}
	return nil
}
