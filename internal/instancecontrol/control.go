// Package instancecontrol coordinates instance maintenance across processes sharing
// a persistent, local state directory. Lock files must never be unlinked.
package instancecontrol

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var (
	ErrMaintenance      = errors.New("instance maintenance in progress; please try again later")
	ErrEmailDisabled    = errors.New("Outbound email is disabled. Contact an administrator to enable it.")
	ErrEmailUnavailable = errors.New("Outbound email status is unavailable. Email is paused until its current policy can be confirmed.")
	ErrOperationBusy    = errors.New("another instance operation is in progress")
)

type Control struct {
	stateDir    string
	owner       ResolutionOwner
	fault       func(string) error
	emailPolicy func(context.Context) (bool, error)
	// Package tests install barriers before starting concurrent work.
	checkpoint func(string)
}

func (c *Control) check(point string) {
	if c.checkpoint != nil {
		c.checkpoint(point)
	}
}

func New(stateDir string) (*Control, error) {
	if stateDir == "" {
		return nil, errors.New("instance state directory is required")
	}
	dir, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("instance state path must be a directory, not a symlink")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	c := &Control{stateDir: dir}
	for _, name := range []string{"admission.lock", "intent.lock", "operation.lock", "email.lock"} {
		f, err := c.openLock(name)
		if err != nil {
			return nil, err
		}
		err = f.Sync()
		_ = f.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := c.syncDir(); err != nil {
		return nil, err
	}
	parent, err := os.Open(filepath.Dir(dir))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	if err := parent.Sync(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Control) StateDir() string { return c.stateDir }

type admissionKey string
type admission struct {
	mu   sync.Mutex
	refs int
	file *os.File
}

func (a *admission) retain() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.refs == 0 {
		return false
	}
	a.refs++
	return true
}
func (a *admission) release() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refs--
	if a.refs == 0 {
		_ = a.file.Close()
	}
}

// Enter rejects new work during maintenance. Nested calls carrying the returned
// context retain the same admission, allowing already-admitted work to drain.
// Fresh readers hold shared intent through their admission decision. That decision
// linearizes before a subsequent maintainer acquires exclusive intent, even when
// scheduling delays the reader's return. Never wait for intent while holding
// admission: maintenance holds intent while draining admitted work.
func (c *Control) Enter(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	key := admissionKey(c.stateDir)
	if a, ok := ctx.Value(key).(*admission); ok && a.retain() {
		return ctx, sync.OnceFunc(a.release), nil
	}
	intent, err := c.openLock("intent.lock")
	if err != nil {
		return ctx, nil, err
	}
	defer intent.Close()
	if err := syscall.Flock(int(intent.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return ctx, nil, fmt.Errorf("%w: %v", ErrMaintenance, err)
	}
	f, err := c.openLock("admission.lock")
	if err != nil {
		return ctx, nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return ctx, nil, fmt.Errorf("%w: %v", ErrMaintenance, err)
	}
	c.check("reader-before-decision")
	if err := c.verifyOpen(); err != nil {
		_ = f.Close()
		return ctx, nil, err
	}
	a := &admission{refs: 1, file: f}
	_ = intent.Close()
	c.check("reader-admitted")
	return context.WithValue(ctx, key, a), sync.OnceFunc(a.release), nil
}

// Maintenance is a closure-only compatibility adapter. Production verification
// uses BeginMaintenance's scoped lease. Releasing these locks never opens access.
func (c *Control) Maintenance(ctx context.Context) (func(), error) {
	l, err := c.BeginMaintenance(ctx)
	if err != nil {
		return nil, err
	}
	return l.Close, nil
}

// KeepClosed only closes. It is not compensation for an unresolved release.
func (c *Control) KeepClosed() error {
	l, err := c.BeginMaintenance(context.Background())
	if err != nil {
		return err
	}
	l.Close()
	return nil
}

// ClearMaintenance no longer grants authority. Legacy callers must use the
// Manager's verified startup/recovery path; absence is never an open decision.
func (c *Control) ClearMaintenance() error { return ErrMaintenance }

func (c *Control) OperationLock() (func(), error) {
	f, err := c.openLock("operation.lock")
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrOperationBusy
		}
		return nil, err
	}
	return sync.OnceFunc(func() { _ = f.Close() }), nil
}

func (c *Control) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, release, err := c.Enter(r.Context())
		if err != nil {
			w.Header().Set("Retry-After", "5")
			http.Error(w, ErrMaintenance.Error(), http.StatusServiceUnavailable)
			return
		}
		defer release()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type emailLeaseKey string

// EnterEmailAfterRefresh establishes ordinary admission before refreshing a
// worker's generation, and only then reads its email policy.
func (c *Control) EnterEmailAfterRefresh(ctx context.Context, refresh func() error) (context.Context, func(), error) {
	if c == nil {
		return ctx, nil, ErrEmailUnavailable
	}
	ctx, leave, err := c.Enter(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if refresh != nil {
		if err = refresh(); err != nil {
			leave()
			return ctx, nil, err
		}
	}
	ctx, release, err := c.EnterEmail(ctx)
	if err != nil {
		leave()
		return ctx, nil, err
	}
	return ctx, sync.OnceFunc(func() { release(); leave() }), nil
}

type EmailStatus string

const (
	EmailEnabled     EmailStatus = "enabled"
	EmailDisabled    EmailStatus = "disabled"
	EmailUnavailable EmailStatus = "unavailable"
)

func (c *Control) EmailStatus(ctx context.Context) EmailStatus {
	err := c.CheckEmail(ctx)
	if err == nil {
		return EmailEnabled
	}
	if errors.Is(err, ErrEmailDisabled) {
		return EmailDisabled
	}
	return EmailUnavailable
}

// EnterEmail retains admission and one context-nested shared email lease through
// handoff. Nested sends reuse the lease rather than waiting behind a transition.
func (c *Control) EnterEmail(ctx context.Context) (context.Context, func(), error) {
	if c == nil {
		return ctx, nil, ErrEmailUnavailable
	}
	ctx, release, err := c.Enter(ctx)
	if err != nil {
		return ctx, nil, err
	}
	key := emailLeaseKey(c.stateDir)
	if a, ok := ctx.Value(key).(*admission); ok && a.retain() {
		return ctx, sync.OnceFunc(func() { a.release(); release() }), nil
	}
	f, err := c.openLock("email.lock")
	if err != nil {
		release()
		return ctx, nil, err
	}
	if err = waitLock(ctx, f, syscall.LOCK_SH); err != nil {
		_ = f.Close()
		release()
		return ctx, nil, err
	}
	if err := c.readEmail(ctx); err != nil {
		_ = f.Close()
		release()
		return ctx, nil, err
	}
	a := &admission{refs: 1, file: f}
	return context.WithValue(ctx, key, a), sync.OnceFunc(func() { a.release(); release() }), nil
}

// EnterEmailTransition must precede authority acquisition. It never upgrades an
// ordinary mail lease or requests exclusive ordinary admission.
func (c *Control) EnterEmailTransition(ctx context.Context) (context.Context, func(), error) {
	if c == nil {
		return ctx, nil, ErrEmailUnavailable
	}
	if ctx.Value(emailLeaseKey(c.stateDir)) != nil {
		return ctx, nil, errors.New("cannot upgrade an email lease")
	}
	ctx, release, err := c.Enter(ctx)
	if err != nil {
		return ctx, nil, err
	}
	f, err := c.openLock("email.lock")
	if err == nil {
		err = waitLock(ctx, f, syscall.LOCK_EX)
	}
	if err != nil {
		if f != nil {
			_ = f.Close()
		}
		release()
		return ctx, nil, err
	}
	return ctx, sync.OnceFunc(func() { _ = f.Close(); release() }), nil
}

// SetEmailPolicy installs the production reader before admission opens. The
// reader refreshes connections before consulting the current database policy.
func (c *Control) SetEmailPolicy(reader func(context.Context) (bool, error)) {
	c.emailPolicy = reader
}

func (c *Control) EmailDisabled() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.CheckEmail(ctx) != nil
}

// LegacyEmailDisabled is an upgrade observation, never modern mail authority.
// ReadDir distinguishes an absent marker from inaccessible directory contents.
func (c *Control) LegacyEmailDisabled() (bool, error) {
	entries, err := os.ReadDir(c.stateDir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.Name() != "email-disabled" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return false, err
		}
		if !info.Mode().IsRegular() {
			return false, errors.New("invalid legacy email marker")
		}
		if _, err := os.ReadFile(filepath.Join(c.stateDir, entry.Name())); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}
func (c *Control) DisableEmail() error { return c.writeMarker("email-disabled") }
func (c *Control) EnableEmail() error  { return c.removeMarker("email-disabled") }
func (c *Control) CheckEmail(ctx context.Context) error {
	_, release, err := c.EnterEmail(ctx)
	if err != nil {
		return err
	}
	defer release()
	return nil
}

func (c *Control) readEmail(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.emailPolicy != nil {
		enabled, err := c.emailPolicy(ctx)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrEmailUnavailable, err)
		}
		if !enabled {
			return ErrEmailDisabled
		}
		return nil
	}
	return ErrEmailUnavailable
}

func (c *Control) openLock(name string) (*os.File, error) {
	fd, err := syscall.Open(filepath.Join(c.stateDir, name), syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func waitLock(ctx context.Context, f *os.File, mode int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func (c *Control) syncDir() error {
	f, err := os.Open(c.stateDir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (c *Control) writeMarker(name string) error {
	f, err := os.CreateTemp(c.stateDir, ".marker-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString("closed\n"); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), filepath.Join(c.stateDir, name)); err != nil {
		return err
	}
	return c.syncDir()
}

func (c *Control) removeMarker(name string) error {
	// Acquire the directory needed for durable removal before changing the gate.
	// Otherwise an open failure (for example write/search but no read permission)
	// reports failure after removing protection, and repairing storage silently
	// enables mail or ordinary admission without a successful explicit retry.
	dir, err := os.Open(c.stateDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	err = os.Remove(filepath.Join(c.stateDir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return dir.Sync()
}
