package instancecontrol

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

// This fake owner qualifies the filesystem protocol, not Manager's DB engine.
// It verifies a separate exact durable summary on every admission.
type admissionFixtureOwner struct {
	c     *Control
	lease *MaintenanceLease
}

var fixtureEvidence = json.RawMessage(`{"verified":"filesystem-fixture"}`)

func (o *admissionFixtureOwner) PrepareAdmission(_ context.Context, l *MaintenanceLease) (json.RawMessage, error) {
	if l != o.lease {
		return nil, ErrMaintenance
	}
	return fixtureEvidence, nil
}
func (o *admissionFixtureOwner) VerifyAdmission(b json.RawMessage) error {
	if string(b) != string(fixtureEvidence) {
		return ErrMaintenance
	}
	f, err := os.Open(filepath.Join(o.c.StateDir(), "fixture-summary"))
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := os.ReadFile(f.Name())
	if err != nil {
		return err
	}
	if string(data) != string(b) {
		return ErrMaintenance
	}
	return f.Sync()
}
func bootstrapAdmissionFixture(t *testing.T, c *Control) *admissionFixtureOwner {
	t.Helper()
	o := &admissionFixtureOwner{c: c}
	if err := c.BindResolutionOwner(o); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.StateDir(), "fixture-summary"), fixtureEvidence, 0600); err != nil {
		t.Fatal(err)
	}
	l, err := c.BeginMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	o.lease = l
	if err = l.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.Close()
	return o
}
func TestPersistentAdmissionMissingCorruptAndStaleDeny(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	deny := func() {
		t.Helper()
		_, release, err := c.Enter(context.Background())
		if release != nil {
			release()
		}
		if !errors.Is(err, ErrMaintenance) {
			t.Fatalf("admitted: %v", err)
		}
	}
	deny()
	bootstrapAdmissionFixture(t, c)
	path := filepath.Join(c.StateDir(), "maintenance")
	valid, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{nil, []byte("closed\n"), []byte(`{"Version":99}`), []byte(`null`)} {
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		deny()
	}
	if err = os.WriteFile(path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(c.StateDir(), "fixture-summary"), []byte("wrong generation"), 0600); err != nil {
		t.Fatal(err)
	}
	deny()
	if err = os.WriteFile(filepath.Join(c.StateDir(), "fixture-summary"), fixtureEvidence, 0600); err != nil {
		t.Fatal(err)
	}
	l, err := c.BeginMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	deny()
}

func TestPersistentAdmissionRejectsDiscriminatorReplacementDuringSync(t *testing.T) {
	for _, replacement := range []string{"inode", "content", "path", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			c, err := New(filepath.Join(t.TempDir(), "state"))
			if err != nil {
				t.Fatal(err)
			}
			bootstrapAdmissionFixture(t, c)
			path := filepath.Join(c.StateDir(), "maintenance")
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			changed := false
			c.checkpoint = func(point string) {
				if point != "admission-read-directory-sync" || changed {
					return
				}
				changed = true
				switch replacement {
				case "inode":
					err = os.WriteFile(path+".replacement", original, 0600)
					if err == nil {
						err = os.Rename(path+".replacement", path)
					}
				case "content":
					err = os.WriteFile(path, []byte("closed\n"), 0600)
				case "path":
					err = os.Rename(c.StateDir(), c.StateDir()+".old")
					if err == nil {
						err = os.Mkdir(c.StateDir(), 0700)
					}
					if err == nil {
						err = os.WriteFile(path, original, 0600)
					}
				case "symlink":
					err = os.Rename(path, path+".old")
					if err == nil {
						err = os.Symlink(path+".old", path)
					}
				}
			}
			_, release, admissionErr := c.Enter(context.Background())
			if release != nil {
				release()
			}
			if err != nil || !changed {
				t.Fatalf("replacement not exercised: changed=%v err=%v", changed, err)
			}
			if !errors.Is(admissionErr, ErrMaintenance) {
				t.Fatalf("replacement admitted: %v", admissionErr)
			}
		})
	}
}

func TestPersistentAdmissionPublicationBarrierAndReconciliation(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := bootstrapAdmissionFixture(t, c)
	l, err := c.BeginMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	o.lease = l
	published, proceed := make(chan struct{}), make(chan struct{})
	c.checkpoint = func(point string) {
		if point == "admission-directory-sync" {
			close(published)
			<-proceed
		}
	}
	done := make(chan error, 1)
	go func() { done <- l.Open(context.Background()) }()
	awaitBarrier(t, published)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, release, err := c.Enter(context.Background())
			if release != nil {
				release()
			}
			if !errors.Is(err, ErrMaintenance) {
				t.Errorf("crossed publication barrier: %v", err)
			}
		}()
	}
	wg.Wait()
	close(proceed)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	c.checkpoint = nil
	l.Close()
	for _, point := range []string{"admission-read", "admission-read-file-sync", "admission-read-directory-sync"} {
		c.fault = func(p string) error {
			if p == point {
				return syscall.EIO
			}
			return nil
		}
		for range 3 {
			_, release, err := c.Enter(context.Background())
			if release != nil {
				release()
			}
			if !errors.Is(err, ErrMaintenance) {
				t.Fatal("persistent EIO admitted", err)
			}
		}
	}
	c.fault = nil
	_, release, err := c.Enter(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestPersistentAdmissionPublicationFaults(t *testing.T) {
	for _, point := range []string{"admission-create", "admission-write", "admission-file-sync", "admission-rename", "admission-directory-sync"} {
		t.Run(point, func(t *testing.T) {
			c, err := New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			o := bootstrapAdmissionFixture(t, c)
			l, err := c.BeginMaintenance(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			o.lease = l
			c.fault = func(p string) error {
				if p == point {
					return syscall.EIO
				}
				return nil
			}
			err = l.Open(context.Background())
			if !errors.Is(err, syscall.EIO) {
				t.Fatal(err)
			}
			if point == "admission-directory-sync" && !errors.Is(err, ErrReleaseUnresolved) {
				t.Fatal("false definite outcome", err)
			}
			l.Close()
			c.fault = nil
			_, release, err := c.Enter(context.Background())
			if release != nil {
				release()
			}
			if point == "admission-directory-sync" {
				if err != nil {
					t.Fatal("verified repair did not reconcile", err)
				}
			} else if !errors.Is(err, ErrMaintenance) {
				t.Fatal("prepublication fault opened", err)
			}
		})
	}
}

func TestPersistentAdmissionScopedLease(t *testing.T) {
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bootstrapAdmissionFixture(t, c)
	l, err := c.BeginMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Open(context.Background()); !errors.Is(err, ErrMaintenance) {
		t.Fatal("unverified lease released", err)
	}
	l.Close()
	if err = l.Open(context.Background()); !errors.Is(err, ErrMaintenance) {
		t.Fatal("expired lease released", err)
	}
}

func TestPersistentAdmissionPermissionFailureRetainsClosed(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires unprivileged filesystem enforcement")
	}
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	o := bootstrapAdmissionFixture(t, c)
	l, err := c.BeginMaintenance(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	o.lease = l
	t.Cleanup(func() { _ = os.Chmod(c.StateDir(), 0700) })
	if err = os.Chmod(c.StateDir(), 0300); err != nil {
		t.Fatal(err)
	}
	if err = l.Open(context.Background()); !errors.Is(err, os.ErrPermission) {
		t.Fatal("expected prepublication permission failure", err)
	}
	if err = os.Chmod(c.StateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := c.readAdmission()
	if err != nil || s.Decision != "closed" {
		t.Fatal("repair implicitly opened", s, err)
	}
	if err = l.Open(context.Background()); err != nil {
		t.Fatal("explicit verified retry failed", err)
	}
}

func TestPersistentAdmissionFreshProcess(t *testing.T) {
	if dir := os.Getenv("FREEFSM_GATE_CHILD"); dir != "" {
		c, err := New(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.BindResolutionOwner(&admissionFixtureOwner{c: c}); err != nil {
			t.Fatal(err)
		}
		c.fault = func(p string) error {
			if p == "admission-read-directory-sync" {
				return syscall.EIO
			}
			return nil
		}
		_, release, err := c.Enter(context.Background())
		if release != nil {
			release()
		}
		if !errors.Is(err, ErrMaintenance) {
			t.Fatal("fresh process EIO admitted", err)
		}
		c.fault = nil
		_, release, err = c.Enter(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		release()
		return
	}
	c, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bootstrapAdmissionFixture(t, c)
	cmd := exec.Command(os.Args[0], "-test.run=^TestPersistentAdmissionFreshProcess$")
	cmd.Env = append(os.Environ(), "FREEFSM_GATE_CHILD="+c.StateDir())
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child: %v %s", err, b)
	}
}
