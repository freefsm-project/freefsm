package instancecontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestEmailCompositionLeases(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bootstrapAdmissionFixture(t, c)
	ctx := context.Background()
	if _, _, err := c.EnterEmail(ctx); !errors.Is(err, ErrEmailUnavailable) {
		t.Fatalf("missing reader: %v", err)
	}
	reads := 0
	c.SetEmailPolicy(func(context.Context) (bool, error) { reads++; return true, nil })
	first, release, err := c.EnterEmail(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	nested, leave, err := c.EnterEmail(first)
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	if reads != 1 {
		t.Fatalf("nested lifecycle reread policy: %d", reads)
	}
	f, err := c.openLock("email.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	blocked := func() {
		t.Helper()
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Fatalf("lease not retained: %v", err)
		}
	}
	blocked()
	release()
	release()
	blocked()
	child, cancel := context.WithCancel(nested)
	cancel()
	if _, _, err := c.EnterEmail(child); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled nesting: %v", err)
	}
	blocked()
	leave()
	leave()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	defer stop()
	if _, _, err := c.EnterEmail(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled wait: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	refreshed := false
	c.SetEmailPolicy(func(context.Context) (bool, error) {
		if !refreshed {
			t.Error("policy preceded generation refresh")
		}
		return true, nil
	})
	_, done, err := c.EnterEmailAfterRefresh(ctx, func() error { refreshed = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	done()
	before, err := os.Stat(filepath.Join(c.StateDir(), "email.lock"))
	if err != nil {
		t.Fatal(err)
	}
	_, done, err = c.EnterEmailTransition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done()
	done()
	after, err := os.Stat(filepath.Join(c.StateDir(), "email.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("email lock replaced: %v", err)
	}
	c.SetEmailPolicy(func(context.Context) (bool, error) { return false, nil })
	if c.EmailStatus(ctx) != EmailDisabled {
		t.Fatal("disabled status")
	}
	c.SetEmailPolicy(func(context.Context) (bool, error) { return false, errors.New("database unavailable") })
	if c.EmailStatus(ctx) != EmailUnavailable {
		t.Fatal("unavailable status")
	}
}
