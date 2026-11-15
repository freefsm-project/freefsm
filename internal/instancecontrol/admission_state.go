package instancecontrol

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

var ErrReleaseUnresolved = errors.New("admission release unresolved")

// ResolutionOwner is the installed backup Manager. PrepareAdmission must verify
// the active lease's installed generation and all cleanup obligations; accepting
// a syntactically valid caller-supplied record is not verification.
type ResolutionOwner interface {
	PrepareAdmission(context.Context, *MaintenanceLease) (json.RawMessage, error)
	VerifyAdmission(json.RawMessage) error
}

// BindResolutionOwner installs the instance's verification owner once, before
// serving requests. New alone never grants admission.
func (c *Control) BindResolutionOwner(owner ResolutionOwner) error {
	if owner == nil || c.owner != nil {
		return ErrMaintenance
	}
	c.owner = owner
	return nil
}

type admissionState struct {
	Version    int
	Decision   string
	Transition string
	Resolution json.RawMessage
}

type MaintenanceLease struct {
	c      *Control
	mu     sync.Mutex
	state  admissionState
	unlock func()
	closed bool
}

func (l *MaintenanceLease) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		l.unlock()
	}
}

// PreviousResolution is retained across closure and process death. It is a
// recovery identity only, never permission to admit work.
func (l *MaintenanceLease) PreviousResolution() json.RawMessage {
	return append(json.RawMessage(nil), l.state.Resolution...)
}

func (l *MaintenanceLease) Transition() string { return l.state.Transition }

// RetainResolution preserves the existing Manager's selected identity before
// journal removal. This can only publish CLOSED, regardless of supplied bytes.
func (l *MaintenanceLease) RetainResolution(b json.RawMessage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.state.Decision != "closed" {
		return ErrMaintenance
	}
	l.state.Resolution = append(json.RawMessage(nil), b...)
	return l.c.writeAdmission(l.state)
}

// Open takes no caller evidence. Only the bound verification owner can supply
// release-ready evidence for this exact live lease.
func (l *MaintenanceLease) Open(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.c.owner == nil || l.state.Decision != "closed" {
		return ErrMaintenance
	}
	b, err := l.c.owner.PrepareAdmission(ctx, l)
	if err != nil {
		return err
	}
	if err = l.c.owner.VerifyAdmission(b); err != nil {
		return err
	}
	next := l.state
	next.Decision, next.Resolution = "verified-open", b
	if err = l.c.writeAdmission(next); err != nil {
		return err
	}
	l.state = next
	return nil
}

func currentFile(f *os.File, path string) error {
	a, err := f.Stat()
	if err != nil {
		return err
	}
	b, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(a, b) || b.Mode()&os.ModeSymlink != 0 {
		return ErrMaintenance
	}
	return nil
}

func (c *Control) admissionFault(point string) error {
	c.check(point)
	if c.fault != nil {
		return c.fault(point)
	}
	return nil
}

func (c *Control) admissionDirectory() (*os.File, error) {
	fd, err := syscall.Open(c.stateDir, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), c.stateDir), nil
}

func (c *Control) readAdmission() (admissionState, error) {
	var s admissionState
	dir, err := c.admissionDirectory()
	if err != nil {
		return s, err
	}
	defer dir.Close()
	path := filepath.Join(c.stateDir, "maintenance")
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return s, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return s, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return s, ErrMaintenance
	}
	if err = c.admissionFault("admission-read"); err != nil {
		return s, err
	}
	b, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil {
		return s, err
	}
	if bytes.Equal(b, []byte("closed\n")) {
		return s, ErrMaintenance
	}
	if len(b) > 65536 || json.Unmarshal(b, &s) != nil {
		return s, ErrMaintenance
	}
	canonical, err := json.Marshal(s)
	id, idErr := hex.DecodeString(s.Transition)
	if err != nil || !bytes.Equal(b, canonical) || s.Version != 1 || idErr != nil || len(id) != 32 || (s.Decision != "closed" && s.Decision != "verified-open") {
		return admissionState{}, ErrMaintenance
	}
	if err = c.admissionFault("admission-read-file-sync"); err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = c.admissionFault("admission-read-directory-sync")
	}
	if err == nil {
		err = dir.Sync()
	}
	if err == nil {
		err = currentFile(f, path)
	}
	if err == nil {
		err = currentFile(dir, c.stateDir)
	}
	if err != nil {
		return admissionState{}, err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return admissionState{}, err
	}
	again, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || !bytes.Equal(b, again) {
		return admissionState{}, ErrMaintenance
	}
	return s, nil
}

func (c *Control) verifyOpen() error {
	s, err := c.readAdmission()
	if err != nil || s.Decision != "verified-open" || c.owner == nil {
		return ErrMaintenance
	}
	if err = c.owner.VerifyAdmission(s.Resolution); err != nil {
		return errors.Join(ErrMaintenance, err)
	}
	return nil
}

// ConfirmResolution observes one exact current verified-open decision. It grants
// no admission lease and performs no recovery. Publication is frozen through the
// matching summary/generation verification, using the same D1 lock order as Enter.
func (c *Control) ConfirmResolution(transition string, want json.RawMessage) error {
	intent, err := c.openLock("intent.lock")
	if err != nil {
		return ErrMaintenance
	}
	defer intent.Close()
	if syscall.Flock(int(intent.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil {
		return ErrMaintenance
	}
	admission, err := c.openLock("admission.lock")
	if err != nil {
		return ErrMaintenance
	}
	defer admission.Close()
	if syscall.Flock(int(admission.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil {
		return ErrMaintenance
	}
	s, err := c.readAdmission()
	if err != nil || s.Decision != "verified-open" || s.Transition != transition || c.owner == nil || !bytes.Equal(s.Resolution, want) {
		return ErrMaintenance
	}
	return c.owner.VerifyAdmission(s.Resolution)
}

func (c *Control) writeAdmission(s admissionState) error {
	dir, err := c.admissionDirectory()
	if err != nil {
		return err
	}
	defer dir.Close()
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err = c.admissionFault("admission-create"); err != nil {
		return err
	}
	f, err := os.CreateTemp(c.stateDir, ".admission-*")
	if err != nil {
		return err
	}
	defer f.Close()
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err != nil {
		return err
	}
	if err = c.admissionFault("admission-write"); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = c.admissionFault("admission-file-sync")
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = currentFile(dir, c.stateDir)
	}
	if err == nil {
		err = c.admissionFault("admission-rename")
	}
	if err != nil {
		return err
	}
	path := filepath.Join(c.stateDir, "maintenance")
	if err = os.Rename(f.Name(), path); err != nil {
		return errors.Join(ErrReleaseUnresolved, err)
	}
	if err = c.admissionFault("admission-directory-sync"); err == nil {
		err = dir.Sync()
	}
	if err == nil {
		err = currentFile(f, path)
	}
	if err == nil {
		err = currentFile(dir, c.stateDir)
	}
	if err != nil {
		return errors.Join(ErrReleaseUnresolved, err)
	}
	return nil
}

// BeginMaintenance retains D1: exclusive intent, durable closure, then drain.
// Caller owns OperationLock for the entire lease.
func (c *Control) BeginMaintenance(ctx context.Context) (*MaintenanceLease, error) {
	intent, err := c.openLock("intent.lock")
	if err != nil {
		return nil, err
	}
	if err = waitLock(ctx, intent, syscall.LOCK_EX); err != nil {
		intent.Close()
		return nil, err
	}
	old, readErr := c.readAdmission()
	// Missing and legacy closed state can bootstrap; malformed modern evidence
	// must be investigated rather than discarded as an absent journal.
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		b, e := os.ReadFile(filepath.Join(c.stateDir, "maintenance"))
		if e != nil || !bytes.Equal(b, []byte("closed\n")) {
			intent.Close()
			return nil, readErr
		}
	}
	var id [32]byte
	if _, err = rand.Read(id[:]); err != nil {
		intent.Close()
		return nil, err
	}
	s := admissionState{Version: 1, Decision: "closed", Transition: hex.EncodeToString(id[:]), Resolution: old.Resolution}
	// Keep a crash-left closed transition stable across recovery retries.
	if old.Decision == "closed" {
		s.Transition = old.Transition
	}
	if err = c.writeAdmission(s); err != nil {
		intent.Close()
		return nil, err
	}
	c.check("maintenance-closed")
	f, err := c.openLock("admission.lock")
	if err != nil {
		intent.Close()
		return nil, err
	}
	if err = waitLock(ctx, f, syscall.LOCK_EX); err != nil {
		f.Close()
		intent.Close()
		return nil, err
	}
	return &MaintenanceLease{c: c, state: s, unlock: func() { f.Close(); intent.Close() }}, nil
}
