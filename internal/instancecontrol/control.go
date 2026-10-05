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
	ErrMaintenance   = errors.New("instance maintenance in progress; please try again later")
	ErrEmailDisabled = errors.New("Outbound email is disabled. Contact an administrator to enable it.")
	ErrOperationBusy = errors.New("another instance operation is in progress")
)

type Control struct{ stateDir string }

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
	for _, name := range []string{"admission.lock", "intent.lock", "operation.lock"} {
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
func (c *Control) Enter(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	key := admissionKey(c.stateDir)
	if a, ok := ctx.Value(key).(*admission); ok && a.retain() {
		return ctx, sync.OnceFunc(a.release), nil
	}
	if c.marker("maintenance") {
		return ctx, nil, ErrMaintenance
	}
	f, err := c.openLock("admission.lock")
	if err != nil {
		return ctx, nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return ctx, nil, fmt.Errorf("%w: %v", ErrMaintenance, err)
	}
	if c.marker("maintenance") {
		_ = f.Close()
		return ctx, nil, ErrMaintenance
	}
	a := &admission{refs: 1, file: f}
	return context.WithValue(ctx, key, a), sync.OnceFunc(a.release), nil
}

// Maintenance persists intent before draining readers. Its release ONLY releases
// locks: the marker remains closed until the engine explicitly calls
// ClearMaintenance after verified success/rollback, while still holding these
// locks and OperationLock. Acquisition works with a crash-left marker present.
// Never call Maintenance from a context currently holding Enter admission.
func (c *Control) Maintenance(ctx context.Context) (func(), error) {
	intent, err := c.openLock("intent.lock")
	if err != nil {
		return nil, err
	}
	if err := waitLock(ctx, intent, syscall.LOCK_EX); err != nil {
		_ = intent.Close()
		return nil, err
	}
	if err := c.writeMarker("maintenance"); err != nil {
		_ = intent.Close()
		return nil, err
	}
	f, err := c.openLock("admission.lock")
	if err != nil {
		_ = intent.Close()
		return nil, err
	}
	if err := waitLock(ctx, f, syscall.LOCK_EX); err != nil {
		_ = f.Close()
		_ = intent.Close()
		return nil, err
	}
	return sync.OnceFunc(func() { _ = f.Close(); _ = intent.Close() }), nil
}

// KeepClosed is also useful if the engine encounters an error after clearing.
func (c *Control) KeepClosed() error       { return c.writeMarker("maintenance") }
func (c *Control) ClearMaintenance() error { return c.removeMarker("maintenance") }

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

// EnterEmail holds admission while checking the persistent email gate. Callers
// must release admission after the complete email lifecycle. A nil Control
// preserves the ungated behavior of services constructed without instance control.
func (c *Control) EnterEmail(ctx context.Context) (context.Context, func(), error) {
	if c == nil {
		return ctx, func() {}, nil
	}
	ctx, release, err := c.Enter(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if err := c.CheckEmail(ctx); err != nil {
		release()
		return ctx, nil, err
	}
	return ctx, release, nil
}

func (c *Control) EmailDisabled() bool { return c.marker("email-disabled") }
func (c *Control) DisableEmail() error { return c.writeMarker("email-disabled") }
func (c *Control) EnableEmail() error  { return c.removeMarker("email-disabled") }
func (c *Control) CheckEmail(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.EmailDisabled() {
		return ErrEmailDisabled
	}
	return nil
}

func (c *Control) marker(name string) bool {
	// A missing/inaccessible state directory is not an enabled state.
	f, err := os.Open(c.stateDir)
	if err != nil {
		return true
	}
	_ = f.Close()
	_, err = os.Lstat(filepath.Join(c.stateDir, name))
	return !errors.Is(err, os.ErrNotExist)
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
	err := os.Remove(filepath.Join(c.stateDir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return c.syncDir()
}
